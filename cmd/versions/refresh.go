package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spin-stack/go-tools/versions"
	"github.com/spin-stack/go-tools/versions/upstream"
)

// refresher moves entries of a versions.yaml to the newest their tracks find, one commit each, and
// with propose makes of those commits a pull request: the shell every repository's refresh task
// wrote for itself, once.
type refresher struct {
	file string
	// git runs git in the repository and answers its output, trimmed.
	git func(args ...string) (string, error)
	// gh is GitHub's API, for propose; nil otherwise.
	gh *github
	// label names the branches a refresh proposes, refresh/<label>-<what they hold>, and which open
	// pull requests a new one supersedes.
	label, base string
	out         io.Writer
	now         time.Time
}

// moved is an entry a refresh moved, and the note that says how to check it.
type moved struct {
	name, from, to, note string
}

// refresh moves each named entry. A bump that fails is said and the others go on: one upstream
// down is no reason to leave the rest behind, and the error the run ends with names it.
func (r refresher) refresh(ctx context.Context, names []string) error {
	if dirty, err := r.git("status", "--porcelain", "--", r.file); err != nil || dirty != "" {
		return cmp.Or(err, fmt.Errorf("refresh: %s has changes of its own: commit or drop them first", r.file))
	}
	start, err := r.git("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	if r.gh != nil {
		if _, err := r.git("switch", "-q", "-c", r.pending()); err != nil {
			return err
		}
	}
	var all, held []moved
	var failed []error
	for _, name := range names {
		m, major, err := r.move(ctx, name)
		if err != nil {
			failed = append(failed, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if major {
			held = append(held, m...)
			continue
		}
		all = append(all, m...)
	}
	if r.gh == nil {
		_, err := io.WriteString(r.out, summary(all, held, failed))
		return errors.Join(append(failed, err)...)
	}
	return errors.Join(append(failed, r.propose(ctx, start, all, held, failed))...)
}

// refresh is the command: r, opening a pull request when propose says to.
func refresh(ctx context.Context, r refresher, propose bool, names []string) error {
	if len(names) == 0 {
		return errors.New("refresh: name the entries it moves")
	}
	if propose {
		var err error
		if r.gh, err = newGitHub(r.git); err != nil {
			return err
		}
	}
	return r.refresh(ctx, names)
}

// move bumps one entry and commits what moved, the entries with it included; nothing when it is
// at its newest. A bump that crosses a major version is not written, and major says so: what
// would have moved is a person's decision, since a schedule that merges what its checks pass
// would otherwise take a new major the day it is tagged.
func (r refresher) move(ctx context.Context, name string) (m []moved, major bool, err error) {
	v, err := versions.Load(r.file)
	if err != nil {
		return nil, false, err
	}
	if named, err := v.Get(name); err != nil {
		return nil, false, err
	} else if err := anothers(named); err != nil {
		return nil, false, err
	}
	e, out, err := upstream.Bump(ctx, v, name, "")
	// Pinned past what its upstream calls the newest - a release candidate, a release its
	// publisher has not marked latest: it is at its newest, as far as a schedule is concerned.
	if errors.Is(err, upstream.ErrBackwards) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	after, err := versions.Parse(out)
	if err != nil { // mutate-exempt: out is what Bump read back whole
		return nil, false, err
	}
	for i, a := range after.Entries {
		if b := v.Entries[i]; a.Version != b.Version || a.Pin != b.Pin {
			m = append(m, moved{a.Name, b.Version, a.Version, a.Note})
		}
	}
	if len(m) == 0 {
		return nil, false, nil
	}
	if slices.ContainsFunc(m, func(x moved) bool { return otherMajor(x.from, x.to) }) {
		return m, true, nil
	}
	if err := os.WriteFile(r.file, out, 0o644); err != nil {
		return nil, false, err
	}
	_, err = r.git("commit", "-q", "-m", "versions: "+e.Name+" "+e.Version, "--", r.file)
	return m, false, err
}

// leading is a version's first number, after a v.
var leading = regexp.MustCompile(`^v?([0-9]+)`)

// otherMajor is whether to's first number is not from's. A version with none (a branch, latest)
// has no major to cross, and neither has a date: a snapshot's or a firmware's first number is
// its year, which a schedule moves every January.
func otherMajor(from, to string) bool {
	a, okA := firstNumber(from)
	b, okB := firstNumber(to)
	return okA && okB && a != b
}

// firstNumber is the version's first number, when it has one that is not a year.
func firstNumber(version string) (int, bool) {
	m := leading.FindStringSubmatch(version)
	if m == nil {
		return 0, false // mutate-exempt: a number with false, which no caller reads
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil && n < 1000
}

// pending is the branch a proposing refresh moves entries on, named for what it holds once it has.
func (r refresher) pending() string { return "refresh/" + r.label + "-pending" } // mutate-exempt: a name the branch has only until it is renamed

// propose pushes what moved as refresh/<label>-<sum of the file> and opens its pull request,
// closing the open ones of the same label it supersedes. A branch named for what it holds is
// proposed once: a person who closed it said no to that set, and it is not asked again.
func (r refresher) propose(ctx context.Context, start string, all, held []moved, failed []error) error {
	if len(all) == 0 {
		said := "refresh: every entry is at its newest\n"
		if len(held) > 0 {
			said = summary(all, held, nil)
		}
		_, err := io.WriteString(r.out, said)
		return errors.Join(err, r.back(start, r.pending()))
	}
	raw, err := os.ReadFile(r.file)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	branch := "refresh/" + r.label + "-" + hex.EncodeToString(sum[:])[:12]
	if _, err := r.git("branch", "-q", "-M", branch); err != nil {
		return err
	}
	if was, err := r.gh.proposed(ctx, branch); err != nil || was != "" {
		if err == nil {
			_, err = fmt.Fprintf(r.out, "refresh: %s was proposed already: %s\n", branch, was)
		}
		return errors.Join(err, r.back(start, branch))
	}
	if _, err := r.git("push", "-q", "-f", "origin", branch); err != nil {
		return err
	}
	pr, err := r.gh.create(ctx, r.base, branch, title(all, r.now), summary(all, held, failed))
	if err != nil {
		return err
	}
	if err := r.say(pr.URL); err != nil {
		return err
	}
	return r.gh.supersede(ctx, "refresh/"+r.label+"-", branch, pr)
}

// say prints line, and in a workflow adds it to the run's summary, where the pull request is
// found without opening the step's log.
func (r refresher) say(line string) error {
	if _, err := fmt.Fprintln(r.out, line); err != nil {
		return err
	}
	path := os.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // the path the runner gives
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, line)
	return errors.Join(err, f.Close())
}

// back returns to the branch the refresh started on and drops made, the one it made for nothing.
func (r refresher) back(start, made string) error {
	if _, err := r.git("switch", "-q", start); err != nil {
		return err
	}
	_, err := r.git("branch", "-q", "-D", made)
	return err
}

// title is one entry's move, or how many moved and the day.
func title(all []moved, now time.Time) string {
	if len(all) == 1 {
		return "versions: " + all[0].name + " " + all[0].to
	}
	return fmt.Sprintf("versions: %d pins as of %s", len(all), now.UTC().Format("2006-01-02"))
}

// summary is what moved, what each note says to check, what is a new major and so a person's,
// and what could not be moved: the pull request's body, or what a refresh without one prints.
func summary(all, held []moved, failed []error) string {
	var b strings.Builder
	switch {
	case len(all) == 0 && len(held) == 0:
		b.WriteString("Every entry is at its newest.\n")
	case len(all) == 0:
		b.WriteString("Nothing moved.\n")
	default:
		b.WriteString("| entry | from | to |\n|---|---|---|\n")
		for _, m := range all {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", m.name, m.from, m.to)
		}
		b.WriteString("\nWhat each one's note says to check before merging:\n\n")
		for _, m := range all {
			fmt.Fprintf(&b, "- **%s**: %s\n", m.name, cmp.Or(m.note, "no note"))
		}
	}
	if len(held) > 0 {
		b.WriteString("\nA new major version, a person's to move (versions bump <name>):\n\n")
		for _, h := range held {
			fmt.Fprintf(&b, "- %s: %s to %s\n", h.name, h.from, h.to)
		}
	}
	if len(failed) > 0 {
		b.WriteString("\nNot moved:\n\n")
		for _, err := range failed {
			fmt.Fprintf(&b, "- %s\n", err)
		}
	}
	return b.String()
}

// runGit is git in the working directory.
func runGit(args ...string) (string, error) {
	cmd := exec.Command("git", args...) //nolint:gosec // git, with the arguments refresh names
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// github is the pull request API of one repository, owner/repo, as token.
type github struct {
	api, repo, token string
}

// pull is a pull request as the API answers one.
type pull struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
	Head   struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

// remote is owner/repo in a GitHub remote's URL, https or ssh.
var remote = regexp.MustCompile(`github\.com[:/]([^/]+/[^/]+?)(?:\.git)?$`)

// newGitHub is the repository GITHUB_REPOSITORY names in a workflow, or origin's elsewhere, as
// GH_TOKEN or GITHUB_TOKEN, in gh's order: a workflow sets GITHUB_TOKEN to the run's own token,
// which may only read, and hands an app's token as GH_TOKEN for what is to start the checks.
func newGitHub(git func(...string) (string, error)) (*github, error) {
	token := cmp.Or(os.Getenv("GH_TOKEN"), os.Getenv("GITHUB_TOKEN"))
	if token == "" {
		return nil, errors.New("refresh: -propose opens a pull request as GH_TOKEN or GITHUB_TOKEN, and neither is set")
	}
	repo := os.Getenv("GITHUB_REPOSITORY")
	if repo == "" {
		origin, err := git("remote", "get-url", "origin")
		if err != nil {
			return nil, err
		}
		m := remote.FindStringSubmatch(origin)
		if m == nil {
			return nil, fmt.Errorf("refresh: origin, %s, is not a GitHub repository", origin)
		}
		repo = m[1]
	}
	return &github{api: "https://api.github.com", repo: repo, token: token}, nil
}

// proposed is the pull request, in any state, whose head is branch; "" when there is none.
func (g *github) proposed(ctx context.Context, branch string) (string, error) {
	owner, _, _ := strings.Cut(g.repo, "/")
	var prs []pull
	q := url.Values{"state": {"all"}, "head": {owner + ":" + branch}}
	if err := g.do(ctx, http.MethodGet, "/pulls?"+q.Encode(), nil, &prs); err != nil {
		return "", err
	}
	if len(prs) == 0 {
		return "", nil
	}
	return prs[0].URL, nil
}

func (g *github) create(ctx context.Context, base, head, title, body string) (pull, error) {
	var pr pull
	err := g.do(ctx, http.MethodPost, "/pulls", map[string]string{"base": base, "head": head, "title": title, "body": body}, &pr)
	return pr, err
}

// supersede closes every open pull request whose branch starts with prefix but pr's, saying which
// replaced it, and deletes its branch.
func (g *github) supersede(ctx context.Context, prefix, keep string, pr pull) error {
	var open []pull
	if err := g.do(ctx, http.MethodGet, "/pulls?state=open&per_page=100", nil, &open); err != nil {
		return err
	}
	for _, o := range open {
		if !strings.HasPrefix(o.Head.Ref, prefix) || o.Head.Ref == keep {
			continue
		}
		n := fmt.Sprint(o.Number)
		if err := errors.Join(
			g.do(ctx, http.MethodPost, "/issues/"+n+"/comments", map[string]string{"body": fmt.Sprintf("Superseded by #%d.", pr.Number)}, nil),
			g.do(ctx, http.MethodPatch, "/pulls/"+n, map[string]string{"state": "closed"}, nil),
			g.do(ctx, http.MethodDelete, "/git/refs/heads/"+o.Head.Ref, nil, nil),
		); err != nil {
			return err
		}
	}
	return nil
}

// do sends body as JSON to the repository's path and decodes the answer into into, when given.
func (g *github) do(ctx context.Context, method, path string, body, into any) error {
	var in io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil { // mutate-exempt: maps of strings, which always marshal
			return err
		}
		in = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.api+"/repos/"+g.repo+path, in)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()     //nolint:errcheck // a read-only body
	if resp.StatusCode >= 300 { // mutate-exempt: GitHub's API answers no 3xx it does not follow itself
		said, _ := io.ReadAll(io.LimitReader(resp.Body, 512)) //nolint:errcheck // what GitHub said, for the error; mutate-exempt: a bound on what an error quotes
		return fmt.Errorf("refresh: %s %s answered %s: %s", method, path, resp.Status, bytes.TrimSpace(said))
	}
	if into == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(into)
}
