package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/internal/gittest"
)

// TestMain fences the git refresh runs: a mutant that loses its way finds no repository, rather
// than the checkout's (internal/gittest).
func TestMain(m *testing.M) {
	gittest.Fence()
	os.Exit(m.Run())
}

func today() string { return time.Now().UTC().Format("20060102") + "T000000Z" }

// refreshFile is an entry behind, one moved with it, and one at its newest: none reaches the
// network to know it.
func refreshFile() string {
	return `- name: snapshot
  kind: date
  version: 20260101T000000Z
  track: today
  note: build everything again
- name: mirror
  kind: date
  version: 20260101T000000Z
  track: with snapshot
- name: current
  kind: date
  version: ` + today() + `
  track: today
`
}

// inRepository is a clone of a bare origin holding file as versions.yaml, at main, as the working
// directory; it answers origin's path.
func inRepository(t *testing.T, file string) string {
	t.Helper()
	origin := t.TempDir()
	gittest.Run(t, origin, "init", "-q", "--bare", "-b", "main")
	dir := t.TempDir()
	gittest.Run(t, dir, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "versions.yaml"), []byte(file), 0o600))
	gittest.Run(t, dir, "add", "versions.yaml")
	gittest.Run(t, dir, "commit", "-q", "-m", "versions")
	gittest.Run(t, dir, "remote", "add", "origin", origin)
	gittest.Run(t, dir, "push", "-q", "origin", "main")
	t.Chdir(dir)
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": gittest.IdentityName, "GIT_AUTHOR_EMAIL": gittest.IdentityEmail,
		"GIT_COMMITTER_NAME": gittest.IdentityName, "GIT_COMMITTER_EMAIL": gittest.IdentityEmail} {
		t.Setenv(k, v)
	}
	return origin
}

func git(t *testing.T, args ...string) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	return gittest.Run(t, dir, args...)
}

// A refresh moves each entry behind to its newest in a commit of its own, the entries with it in
// that commit, and says what moved and what each note asks; an entry it cannot move is said and
// the rest still move. Asked again, nothing is behind and nothing is committed.
func TestARefreshCommitsEachEntryItMoved(t *testing.T) {
	inRepository(t, refreshFile())
	var out bytes.Buffer
	r := refresher{file: "versions.yaml", git: runGit, label: "versions", base: "main", out: &out}
	err := r.refresh(t.Context(), []string{"snapshot", "nope", "current"})
	require.ErrorContains(t, err, "nope: versions: there is no nope")
	assert.Equal(t, "versions: snapshot "+today(), git(t, "log", "-1", "--format=%s"))
	assert.Equal(t, "2", git(t, "rev-list", "--count", "HEAD"), "one commit for snapshot and mirror, none for current")
	for _, want := range []string{
		"| snapshot | 20260101T000000Z | " + today() + " |",
		"| mirror | 20260101T000000Z | " + today() + " |",
		"- **snapshot**: build everything again",
		"- **mirror**: no note",
		"Not moved:\n\n- nope: versions: there is no nope",
	} {
		assert.Contains(t, out.String(), want)
	}
	assert.NotContains(t, out.String(), "| current |")

	out.Reset()
	require.NoError(t, r.refresh(t.Context(), []string{"snapshot", "current"}))
	assert.Equal(t, "Every entry is at its newest.\n", out.String())
	assert.Equal(t, "2", git(t, "rev-list", "--count", "HEAD"))
}

// A versions.yaml with changes of its own is not one a refresh commits over.
func TestARefreshLeavesAChangedFileAlone(t *testing.T) {
	inRepository(t, refreshFile())
	require.NoError(t, os.WriteFile("versions.yaml", []byte(refreshFile()+"# mine\n"), 0o600))
	r := refresher{file: "versions.yaml", git: runGit, label: "versions", out: io.Discard}
	assert.ErrorContains(t, r.refresh(t.Context(), []string{"snapshot"}), "versions.yaml has changes of its own")
}

// fakeGitHub is the pull request API of o/r: the pull requests it already has, and every request
// it was sent as "METHOD path" with its body.
type fakeGitHub struct {
	mu       sync.Mutex
	asked    []string
	proposed []pull
	open     []pull
	// fail is a path GitHub answers with an error.
	fail string
}

func (f *fakeGitHub) serve(t *testing.T) *github {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // a test server's own request
		f.mu.Lock()
		defer f.mu.Unlock()
		assert.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
		f.asked = append(f.asked, strings.TrimSpace(r.Method+" "+r.URL.RequestURI()+" "+string(body)))
		switch {
		case r.URL.Path == f.fail:
			http.Error(w, "Not Found", http.StatusNotFound)
		case r.Method == http.MethodGet && r.URL.Query().Get("state") == "all":
			_ = json.NewEncoder(w).Encode(f.proposed) //nolint:errcheck // a test server's answer
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(f.open) //nolint:errcheck // a test server's answer
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/pulls":
			// The one it opens is open from then on, and no refresh supersedes its own.
			var created struct {
				Head string `json:"head"`
			}
			_ = json.Unmarshal(body, &created) //nolint:errcheck // the code under test's own JSON
			f.open = append(f.open, openPull(9, created.Head))
			_, _ = w.Write([]byte(`{"number":9,"html_url":"https://github.com/o/r/pull/9"}`)) //nolint:errcheck // a test server's answer
		}
	}))
	t.Cleanup(srv.Close)
	return &github{api: srv.URL, repo: "o/r", token: "tok"}
}

func openPull(n int, ref string) pull {
	p := pull{Number: n}
	p.Head.Ref = ref
	return p
}

// A proposing refresh pushes what moved as a branch named for the file it leaves, opens its pull
// request with what moved in the body, and closes the open refresh it supersedes - its comment,
// its state, its branch - and no other pull request.
func TestARefreshProposesWhatMoved(t *testing.T) {
	origin := inRepository(t, refreshFile())
	f := &fakeGitHub{open: []pull{openPull(3, "refresh/versions-old"), openPull(4, "feature"), openPull(5, "refresh/kernel-1")}}
	var out bytes.Buffer
	summaryFile := filepath.Join(t.TempDir(), "summary.md")
	t.Setenv("GITHUB_STEP_SUMMARY", summaryFile)
	now := time.Date(2026, 11, 18, 23, 0, 0, 0, time.FixedZone("ART", -3*3600))
	r := refresher{file: "versions.yaml", git: runGit, gh: f.serve(t), label: "versions", base: "main", out: &out, now: now}
	require.NoError(t, r.refresh(t.Context(), []string{"snapshot"}))

	branch := git(t, "rev-parse", "--abbrev-ref", "HEAD")
	assert.Regexp(t, `^refresh/versions-[0-9a-f]{12}$`, branch)
	assert.Equal(t, git(t, "rev-parse", "HEAD"), gittest.Run(t, origin, "rev-parse", branch), "pushed")
	assert.Equal(t, "https://github.com/o/r/pull/9\n", out.String())
	written, err := os.ReadFile(summaryFile)
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/o/r/pull/9\n", string(written), "the run's summary has it too")
	require.Len(t, f.asked, 6)
	assert.Equal(t, "GET /repos/o/r/pulls?head=o%3A"+strings.ReplaceAll(branch, "/", "%2F")+"&state=all", f.asked[0])
	var created map[string]string
	require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(f.asked[1], "POST /repos/o/r/pulls ")), &created))
	assert.Equal(t, "main", created["base"])
	assert.Equal(t, branch, created["head"])
	assert.Equal(t, "versions: 2 pins as of 2026-11-19", created["title"])
	assert.Contains(t, created["body"], "| snapshot | 20260101T000000Z | "+today()+" |")
	assert.Equal(t, []string{
		"GET /repos/o/r/pulls?state=open&per_page=100",
		`POST /repos/o/r/issues/3/comments {"body":"Superseded by #9."}`,
		`PATCH /repos/o/r/pulls/3 {"state":"closed"}`,
		"DELETE /repos/o/r/git/refs/heads/refresh/versions-old",
	}, f.asked[2:])
}

// A set proposed once, open or closed, is not proposed again; and a refresh that moved nothing
// proposes nothing. Either way it leaves the branch it started on as it was, and no other.
func TestARefreshProposesASetOnce(t *testing.T) {
	inRepository(t, refreshFile())
	f := &fakeGitHub{proposed: []pull{{Number: 2, URL: "https://github.com/o/r/pull/2"}}}
	var out bytes.Buffer
	r := refresher{file: "versions.yaml", git: runGit, gh: f.serve(t), label: "versions", base: "main", out: &out}
	start := git(t, "rev-parse", "HEAD")
	require.NoError(t, r.refresh(t.Context(), []string{"snapshot"}))
	assert.Contains(t, out.String(), "was proposed already: https://github.com/o/r/pull/2")
	assert.Len(t, f.asked, 1)

	out.Reset()
	require.NoError(t, r.refresh(t.Context(), []string{"current"}))
	assert.Equal(t, "refresh: every entry is at its newest\n", out.String())
	assert.Len(t, f.asked, 1)

	assert.Equal(t, "main", git(t, "rev-parse", "--abbrev-ref", "HEAD"))
	assert.Equal(t, start, git(t, "rev-parse", "HEAD"))
	assert.Equal(t, "main", git(t, "branch", "--format=%(refname:short)"))
}

// majorFile is two entries tracked with others already moved: one within its major, one to the
// next.
const majorFile = `- name: two
  kind: date
  version: v2.1.0
  track: today
- name: one
  kind: date
  version: v1.4.0
  track: today
- name: next
  kind: date
  version: v1.9.0
  track: with two
- name: patch
  kind: date
  version: v1.0.0
  track: with one
`

// A bump to another major is not committed: it is said as a person's, and the rest still move.
func TestARefreshLeavesANewMajorToAPerson(t *testing.T) {
	inRepository(t, majorFile)
	var out bytes.Buffer
	r := refresher{file: "versions.yaml", git: runGit, label: "versions", base: "main", out: &out}
	require.NoError(t, r.refresh(t.Context(), []string{"next", "patch"}))
	assert.Equal(t, "versions: patch v1.4.0", git(t, "log", "-1", "--format=%s"))
	assert.Equal(t, "2", git(t, "rev-list", "--count", "HEAD"), "one commit, for patch")
	assert.Empty(t, git(t, "status", "--porcelain"))
	assert.Contains(t, out.String(), "| patch | v1.0.0 | v1.4.0 |")
	assert.Contains(t, out.String(), "a person's to move (versions bump <name>):\n\n- next: v1.9.0 to v2.1.0\n")
	assert.NotContains(t, out.String(), "| next |")
}

// A proposing refresh whose only move is a new major opens nothing, says it, and is back where it
// started.
func TestARefreshOfOnlyANewMajorProposesNothing(t *testing.T) {
	inRepository(t, majorFile)
	f := &fakeGitHub{}
	var out bytes.Buffer
	r := refresher{file: "versions.yaml", git: runGit, gh: f.serve(t), label: "versions", base: "main", out: &out}
	start := git(t, "rev-parse", "HEAD")
	require.NoError(t, r.refresh(t.Context(), []string{"next"}))
	assert.Equal(t, "Nothing moved.\n\nA new major version, a person's to move (versions bump <name>):\n\n- next: v1.9.0 to v2.1.0\n", out.String())
	assert.Empty(t, f.asked)
	assert.Equal(t, start, git(t, "rev-parse", "HEAD"))
	assert.Equal(t, "main", git(t, "branch", "--format=%(refname:short)"))
}

// A major is the first number, after a v; a date's or a name's is none to cross.
func TestAMajorIsTheFirstNumber(t *testing.T) {
	for _, c := range []struct {
		from, to string
		want     bool
	}{
		{"v13.3.0", "v14.0.0", true},
		{"13.3.0", "13.3.1", false},
		{"1.27.4-bookworm", "2.0.0-trixie", true},
		{"v0.9.3", "v0.10.0", false},
		{"20260101T000000Z", "20270101T000000Z", false},
		{"999.1.0", "1000.0.0", false},
		{"latest", "latest", false},
		{"main", "v2.0.0", false},
	} {
		assert.Equal(t, c.want, otherMajor(c.from, c.to), "%s to %s", c.from, c.to)
	}
}

// One entry's pull request is named for its move.
func TestOneEntrysRefreshIsNamedForIt(t *testing.T) {
	assert.Equal(t, "versions: kernel 7.3.6", title([]moved{{name: "kernel", to: "7.3.6"}}, time.Now()))
}

// The repository is the workflow's, or origin's on GitHub, and the token is the environment's: a
// refresh with neither has no pull request to open.
func TestTheRepositoryIsTheWorkflowsOrOrigins(t *testing.T) {
	origin := func(url string) func(...string) (string, error) {
		return func(...string) (string, error) { return url, nil }
	}
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	_, err := newGitHub(origin("git@github.com:o/r.git"))
	assert.ErrorContains(t, err, "neither is set")

	t.Setenv("GH_TOKEN", "gh")
	t.Setenv("GITHUB_REPOSITORY", "")
	for url, want := range map[string]string{"git@github.com:o/r.git": "o/r", "https://github.com/o/r": "o/r", "https://github.com/o/r.git": "o/r"} {
		g, err := newGitHub(origin(url))
		require.NoError(t, err, url)
		assert.Equal(t, &github{api: "https://api.github.com", repo: want, token: "gh"}, g, url)
	}
	_, err = newGitHub(origin("https://gitlab.com/o/r"))
	assert.ErrorContains(t, err, "is not a GitHub repository")

	// GH_TOKEN first, as gh reads them: a workflow's GITHUB_TOKEN is its run's, which may only read.
	t.Setenv("GITHUB_TOKEN", "workflow")
	t.Setenv("GITHUB_REPOSITORY", "w/x")
	g, err := newGitHub(origin("unused"))
	require.NoError(t, err)
	assert.Equal(t, &github{api: "https://api.github.com", repo: "w/x", token: "gh"}, g)
	t.Setenv("GH_TOKEN", "")
	g, err = newGitHub(origin("unused"))
	require.NoError(t, err)
	assert.Equal(t, "workflow", g.token)
}

// GitHub refusing a request is an error that says what it was asked and what it answered.
func TestGitHubsRefusalIsSaid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Validation Failed", http.StatusUnprocessableEntity)
	}))
	t.Cleanup(srv.Close)
	g := &github{api: srv.URL, repo: "o/r", token: "tok"}
	_, err := g.create(t.Context(), "main", "b", "t", "")
	assert.ErrorContains(t, err, "POST /pulls answered 422 Unprocessable Entity: Validation Failed")
}

// A refresh that is to propose and has no token opens nothing and moves nothing.
func TestAProposingRefreshNeedsAToken(t *testing.T) {
	inRepository(t, refreshFile())
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	r := refresher{file: "versions.yaml", git: runGit, label: "versions", out: io.Discard}
	assert.ErrorContains(t, refresh(t.Context(), r, true, []string{"snapshot"}), "neither is set")
	assert.Equal(t, "1", git(t, "rev-list", "--count", "HEAD"))
	require.NoError(t, refresh(t.Context(), r, false, []string{"snapshot"}))
	assert.Equal(t, "2", git(t, "rev-list", "--count", "HEAD"))
}

// git that fails says what it said, not only its status.
func TestAGitThatFailsSaysWhy(t *testing.T) {
	inRepository(t, refreshFile())
	_, err := runGit("rev-parse", "--verify", "-q", "--end-of-options", "nothing^{commit}")
	require.Error(t, err)
	_, err = runGit("switch", "nothing-at-all")
	assert.ErrorContains(t, err, "fatal:")
}

// A pull request it superseded that GitHub would not close is the run's error: the person reading
// the run is the one to close it.
func TestASupersededPullThatStaysOpenIsSaid(t *testing.T) {
	inRepository(t, refreshFile())
	f := &fakeGitHub{open: []pull{openPull(3, "refresh/versions-old")}, fail: "/repos/o/r/pulls/3"}
	r := refresher{file: "versions.yaml", git: runGit, gh: f.serve(t), label: "versions", base: "main", out: io.Discard}
	assert.ErrorContains(t, r.refresh(t.Context(), []string{"snapshot"}), "PATCH /pulls/3 answered 404")
}

// An entry pinned past what its upstream calls the newest is at its newest to a refresh: it is
// not moved back, and it is no failure of the run.
func TestARefreshLeavesAPinPastItsNewestWhereItIs(t *testing.T) {
	latestRelease(t, "v1.9.0")
	inRepository(t, aheadFile)
	var out bytes.Buffer
	r := refresher{file: "versions.yaml", git: runGit, label: "versions", base: "main", out: &out}
	require.NoError(t, r.refresh(t.Context(), []string{"tool"}))
	assert.Equal(t, "Every entry is at its newest.\n", out.String())
	assert.Equal(t, "1", git(t, "rev-list", "--count", "HEAD"))
	assert.Empty(t, git(t, "status", "--porcelain"))
}

// An entry another command moves is not one a refresh moves: it is said, with the command, and the
// others still move.
func TestARefreshLeavesAnEntryAnotherCommandMoves(t *testing.T) {
	inRepository(t, strings.Replace(refreshFile(), "  note: build everything again\n", "  bump: go tool task sources:bump\n", 1)+
		"- name: plain\n  kind: date\n  version: 20260101T000000Z\n  track: today\n")
	var out bytes.Buffer
	r := refresher{file: "versions.yaml", git: runGit, label: "versions", base: "main", out: &out}
	err := r.refresh(t.Context(), []string{"snapshot", "plain"})
	require.ErrorContains(t, err, "snapshot: versions: snapshot is moved by `go tool task sources:bump`")
	assert.Equal(t, "versions: plain "+today(), git(t, "log", "-1", "--format=%s"))
	assert.Equal(t, "2", git(t, "rev-list", "--count", "HEAD"), "one commit, for plain")
	assert.NotContains(t, out.String(), "| snapshot |")
}

// Run by the command the entry names, which says so (-by), the refresh moves it: a scheduled
// refresh a repository's own command starts is not stopped by the field that names that command.
func TestARefreshByTheCommandAnEntryNamesMovesIt(t *testing.T) {
	inRepository(t, strings.Replace(refreshFile(), "  note: build everything again\n", "  bump: spin-machine-dev refresh\n", 1))
	var out bytes.Buffer
	r := refresher{file: "versions.yaml", by: "spin-machine-dev refresh", git: runGit, label: "versions", base: "main", out: &out}
	require.NoError(t, r.refresh(t.Context(), []string{"snapshot"}))
	assert.Contains(t, out.String(), "| snapshot |")
}
