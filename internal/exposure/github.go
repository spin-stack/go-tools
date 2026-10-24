package exposure

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// GitHub reads what a repository shows besides its commits.
type GitHub struct {
	API   string // https://api.github.com, or a test's server
	Token string
	HTTP  *http.Client
}

// Surfaces is what the GitHub side of a repository would show: findings in issue and pull
// request titles and bodies, their comments and review comments, and release notes; and the
// look-at lines for what is published whole and cannot be read here - Actions' run logs and
// artifacts, release assets.
func (g GitHub) Surfaces(ctx context.Context, repo string, patterns []Pattern) ([]Finding, []string, error) {
	var t tally
	scan := func(where, text string) {
		for _, m := range Scan(patterns, text) {
			t.add(m.Kind, m.Value, where, true)
		}
	}
	var issues []struct {
		Number      int
		Title, Body string
		PullRequest *struct{} `json:"pull_request"`
	}
	if err := g.all(ctx, "/repos/"+repo+"/issues?state=all&per_page=100", &issues); err != nil {
		return nil, nil, err
	}
	prs := 0
	for _, i := range issues {
		what := "issue"
		if i.PullRequest != nil {
			what = "pull request"
			prs++
		}
		scan(fmt.Sprintf("%s #%d", what, i.Number), i.Title+"\n"+i.Body)
	}
	for _, path := range []string{"/issues/comments", "/pulls/comments"} {
		var comments []struct {
			URL  string `json:"html_url"`
			Body string
		}
		if err := g.all(ctx, "/repos/"+repo+path+"?per_page=100", &comments); err != nil {
			return nil, nil, err
		}
		for _, c := range comments {
			scan(c.URL, c.Body)
		}
	}
	var releases []struct {
		TagName string `json:"tag_name"`
		Name    string
		Body    string
		Assets  []struct{ Name string }
	}
	if err := g.all(ctx, "/repos/"+repo+"/releases?per_page=100", &releases); err != nil {
		return nil, nil, err
	}
	assets := 0
	for _, r := range releases {
		scan("release "+r.TagName, r.Name+"\n"+r.Body)
		assets += len(r.Assets)
	}
	var runs, artifacts struct {
		TotalCount int `json:"total_count"`
	}
	if err := g.get(ctx, "/repos/"+repo+"/actions/runs?per_page=1", &runs, nil); err != nil {
		return nil, nil, err
	}
	if err := g.get(ctx, "/repos/"+repo+"/actions/artifacts?per_page=1", &artifacts, nil); err != nil {
		return nil, nil, err
	}
	look := []string{
		fmt.Sprintf("%d issue(s) and %d pull request(s), their comments and review comments: read for the patterns above, and a person reads what no pattern names", len(issues)-prs, prs),
		fmt.Sprintf("%d Actions run(s): their logs turn public with the repository, and nothing here reads them - delete the ones nobody needs before the switch", runs.TotalCount),
		fmt.Sprintf("%d Actions artifact(s): public with the repository - delete the ones nobody needs", artifacts.TotalCount),
		fmt.Sprintf("%d release(s) with %d asset(s): the notes are read here, the assets are not", len(releases), assets),
	}
	return t.findings(), look, nil
}

// PrivateRepos is the names of the owner's repositories that are private, other than this one:
// a link into one 404s for a reader of a public repository.
func (g GitHub) PrivateRepos(ctx context.Context, owner, except string) ([]string, error) {
	var repos []struct {
		Name    string
		Private bool
	}
	if err := g.all(ctx, "/orgs/"+owner+"/repos?type=all&per_page=100", &repos); err != nil {
		return nil, err
	}
	var out []string
	for _, r := range repos {
		if r.Private && r.Name != except {
			out = append(out, r.Name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// next is the URL a Link header names as the next page.
var next = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// all decodes every page of a listing into into, a pointer to a slice, following Link.
func (g GitHub) all(ctx context.Context, path string, into any) error {
	var items []json.RawMessage
	for url := g.API + path; url != ""; {
		var page []json.RawMessage
		link := ""
		if err := g.get(ctx, url, &page, &link); err != nil {
			return err
		}
		items = append(items, page...)
		url = ""
		if m := next.FindStringSubmatch(link); m != nil {
			url = m[1]
		}
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}

// get decodes one response into into, and gives its Link header to link when asked.
func (g GitHub) get(ctx context.Context, url string, into any, link *string) error {
	if strings.HasPrefix(url, "/") {
		url = g.API + url
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	client := g.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }() // read-only
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512)) // mutate-exempt: how much of a refusal is quoted
		return fmt.Errorf("GET %s: %s: %s", url, resp.Status, strings.TrimSpace(string(body)))
	}
	if link != nil {
		*link = resp.Header.Get("Link")
	}
	return json.NewDecoder(resp.Body).Decode(into)
}
