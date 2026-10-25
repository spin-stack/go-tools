package proven

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// GitHub reads one repository's runs, jobs, logs and commits.
type GitHub struct {
	API   string // https://api.github.com, or a test's server
	Repo  string // owner/name
	Token string
	HTTP  *http.Client
}

// Runs is the newest limit runs of workflow (its file name) matching query - branch, status,
// head_sha, as the API names them - newest first.
func (g GitHub) Runs(ctx context.Context, workflow string, query url.Values, limit int) ([]Run, error) {
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	q.Set("per_page", strconv.Itoa(limit))
	var page struct {
		Runs []Run `json:"workflow_runs"`
	}
	if err := g.get(ctx, "/repos/"+g.Repo+"/actions/workflows/"+workflow+"/runs?"+q.Encode(), &page, nil); err != nil {
		return nil, err
	}
	return page.Runs, nil
}

// Tree is the tree of the commit sha.
func (g GitHub) Tree(ctx context.Context, sha string) (string, error) {
	var c struct {
		Commit struct {
			Tree struct {
				SHA string `json:"sha"`
			} `json:"tree"`
		} `json:"commit"`
	}
	if err := g.get(ctx, "/repos/"+g.Repo+"/commits/"+sha, &c, nil); err != nil {
		return "", err
	}
	return c.Commit.Tree.SHA, nil
}

// Job is one job of a run.
type Job struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"` // success, failure, cancelled, skipped; empty until completed
}

// Artifact is one file a run kept: a lane's logs, when it failed.
type Artifact struct {
	Name    string `json:"name"`
	URL     string `json:"archive_download_url"`
	Expired bool   `json:"expired"`
}

// Artifacts is every artifact of run, over however many pages.
func (g GitHub) Artifacts(ctx context.Context, run int64) ([]Artifact, error) {
	var all []Artifact
	for u := "/repos/" + g.Repo + "/actions/runs/" + strconv.FormatInt(run, 10) + "/artifacts?per_page=100"; u != ""; { // mutate-exempt: the page size, which only how many pages are asked sees
		var page struct {
			Artifacts []Artifact `json:"artifacts"`
		}
		var link string
		if err := g.get(ctx, u, &page, &link); err != nil {
			return nil, err
		}
		all = append(all, page.Artifacts...)
		u = ""
		if m := next.FindStringSubmatch(link); m != nil {
			u = m[1]
		}
	}
	return all, nil
}

// Archive is an artifact's zip, by the URL the listing gave: the API redirects to where the bytes
// are, and the client follows.
func (g GitHub) Archive(ctx context.Context, u string) ([]byte, error) {
	resp, err := g.do(ctx, u, "")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }() // read-only
	return io.ReadAll(resp.Body)
}

// Jobs is every job of run, over however many pages.
func (g GitHub) Jobs(ctx context.Context, run int64) ([]Job, error) {
	var all []Job
	for u := "/repos/" + g.Repo + "/actions/runs/" + strconv.FormatInt(run, 10) + "/jobs?per_page=100"; u != ""; {
		var page struct {
			Jobs []Job `json:"jobs"`
		}
		var link string
		if err := g.get(ctx, u, &page, &link); err != nil {
			return nil, err
		}
		all = append(all, page.Jobs...)
		u = ""
		if m := next.FindStringSubmatch(link); m != nil {
			u = m[1]
		}
	}
	return all, nil
}

// Log is the log of job: the API redirects to where the text is, and the client follows.
func (g GitHub) Log(ctx context.Context, job int64) (string, error) {
	resp, err := g.do(ctx, "/repos/"+g.Repo+"/actions/jobs/"+strconv.FormatInt(job, 10)+"/logs", "")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }() // read-only
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

// next is the URL a Link header names as the next page.
var next = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// get decodes one JSON response into into, and gives its Link header to link when asked.
func (g GitHub) get(ctx context.Context, u string, into any, link *string) error {
	resp, err := g.do(ctx, u, "application/vnd.github+json")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }() // read-only
	if link != nil {
		*link = resp.Header.Get("Link")
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// do is one GET that answered 200; anything else is an error naming what answered what.
func (g GitHub) do(ctx context.Context, u, accept string) (*http.Response, error) {
	if strings.HasPrefix(u, "/") {
		u = g.API + u
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if g.Token != "" {
		// The client drops it on a redirect to another host: a log's signed URL takes none.
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	client := g.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512)) // mutate-exempt: how much of a refusal is quoted
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			return nil, fmt.Errorf("GET %s: %s: %s: %w", u, resp.Status, strings.TrimSpace(string(body)), ErrGone)
		}
		return nil, fmt.Errorf("GET %s: %s: %s", u, resp.Status, strings.TrimSpace(string(body)))
	}
	return resp, nil
}

// ErrGone is what the API answers for a log or an artifact it no longer holds: a job's log is
// kept for a while, then its blob is deleted, and a run from last week has jobs whose logs are a
// 404 in the API and a BlobNotFound where they were. What is gone says nothing; it is no reason
// to stop reading the rest.
var ErrGone = errors.New("no longer held")

// TreesOf is a Trees that asks the clone at root first - free, and the commit is there for
// anything on main - and the API for a commit the clone has not: a pull request's head.
func TreesOf(root string, g GitHub) Trees {
	return func(ctx context.Context, sha string) (string, error) {
		out, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--verify", "--quiet", sha+"^{tree}").Output()
		if err == nil {
			return strings.TrimSpace(string(out)), nil
		}
		return g.Tree(ctx, sha)
	}
}

// FirstParent is the newest n commits of main at root from sha, first-parent, newest first, each
// with its tree.
func FirstParent(ctx context.Context, root, sha string, n int) ([]Commit, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "log", "--first-parent", "--format=%H %T", "-n", strconv.Itoa(n), sha).Output()
	if err != nil {
		return nil, fmt.Errorf("git log %s in %s: %w", sha, root, err)
	}
	var commits []Commit
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if h, t, ok := strings.Cut(line, " "); ok {
			commits = append(commits, Commit{SHA: h, Tree: t})
		}
	}
	return commits, nil
}
