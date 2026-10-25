package proven_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/internal/proven"
)

// github is a server answering as the API does for one repository, recording what it was asked.
func github(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.RequestURI())
		assert.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/repos/o/r/actions/workflows/ci.yml/runs":
			assert.Equal(t, "application/vnd.github+json", r.Header.Get("Accept"))
			_ = json.NewEncoder(w).Encode(map[string]any{"workflow_runs": []proven.Run{
				{ID: 2, HeadSHA: "b", Event: "schedule", Status: "completed", Conclusion: "success", URL: "u2"},
				{ID: 1, HeadSHA: "a", Event: "push", Status: "in_progress", URL: "u1"},
			}})
		case "/repos/o/r/commits/abc":
			_, _ = w.Write([]byte(`{"sha": "abc", "commit": {"tree": {"sha": "tree-abc"}}}`))
		case "/repos/o/r/actions/runs/17/jobs":
			if r.URL.Query().Get("page") == "" {
				w.Header().Set("Link", `<`+"http://"+r.Host+`/repos/o/r/actions/runs/17/jobs?per_page=100&page=2>; rel="next", <x>; rel="last"`)
				_, _ = w.Write([]byte(`{"jobs": [{"id": 71, "name": "short"}]}`))
			} else {
				_, _ = w.Write([]byte(`{"jobs": [{"id": 72, "name": "e2e-verify:1/2"}]}`))
			}
		case "/repos/o/r/actions/jobs/72/logs":
			http.Redirect(w, r, "/blob/72", http.StatusFound)
		case "/blob/72":
			_, _ = w.Write([]byte("--- PASS: TestA (1.5s)\n"))
		default:
			http.Error(w, `{"message": "Not Found"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

// The client asks one repository's API with the token, for its runs (as many as asked, with the
// query), a commit's tree, a run's jobs over every page, and a job's log where the API redirects;
// an answer that is not 200 is an error that says what answered what.
func TestTheClientReadsOneRepository(t *testing.T) {
	srv, asked := github(t)
	gh := proven.GitHub{API: srv.URL, Repo: "o/r", Token: "tok"}
	ctx := t.Context()

	runs, err := gh.Runs(ctx, "ci.yml", url.Values{"status": {"success"}, "branch": {"main"}}, 40)
	require.NoError(t, err)
	assert.Equal(t, []int64{2, 1}, []int64{runs[0].ID, runs[1].ID})
	assert.True(t, runs[0].Passed())
	assert.False(t, runs[1].Passed())
	assert.Equal(t, "/repos/o/r/actions/workflows/ci.yml/runs?branch=main&per_page=40&status=success", (*asked)[0])

	tree, err := gh.Tree(ctx, "abc")
	require.NoError(t, err)
	assert.Equal(t, "tree-abc", tree)

	jobs, err := gh.Jobs(ctx, 17)
	require.NoError(t, err)
	assert.Equal(t, []proven.Job{{ID: 71, Name: "short"}, {ID: 72, Name: "e2e-verify:1/2"}}, jobs)

	log, err := gh.Log(ctx, 72)
	require.NoError(t, err)
	assert.Equal(t, "--- PASS: TestA (1.5s)\n", log)

	_, err = gh.Tree(ctx, "nope")
	require.ErrorContains(t, err, "404 Not Found: {\"message\": \"Not Found\"}")
	_, err = gh.Jobs(ctx, 8)
	require.ErrorContains(t, err, "/actions/runs/8/jobs")
	_, err = gh.Log(ctx, 9)
	require.ErrorContains(t, err, "/actions/jobs/9/logs")
	_, err = gh.Runs(ctx, "x.yml", nil, 1)
	require.ErrorContains(t, err, "x.yml")
}

// clone is a repository with a chain of commits, each a file more, and a commit on a branch with
// the second's tree: a pull request rebased onto an unmoved main, as it merges.
func clone(t *testing.T) (root string, main []string, pr string) {
	t.Helper()
	root = t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return string(out)
	}
	git("init", "-q", "-b", "main")
	for _, name := range []string{"one", "two", "three"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(name), 0o600))
		git("add", name)
		git("commit", "-q", "-m", name)
	}
	git("checkout", "-q", "-b", "pr", "HEAD~1")
	git("commit", "-q", "--allow-empty", "-m", "the same tree, another commit")
	git("checkout", "-q", "main")
	for _, rev := range []string{"main", "main~1", "main~2"} {
		main = append(main, git("rev-parse", rev)[:40])
	}
	return root, main, git("rev-parse", "pr")[:40]
}

// A commit's tree is the clone's answer when the clone has the commit, and the API's when it has
// not. Main's history is its first-parent commits, newest first, each with its tree, as many as
// asked; a commit the clone has not is an error.
func TestTreesComeFromTheCloneThenTheAPI(t *testing.T) {
	root, main, pr := clone(t)
	srv, asked := github(t)
	trees := proven.TreesOf(root, proven.GitHub{API: srv.URL, Repo: "o/r", Token: "tok"})
	ctx := t.Context()

	history, err := proven.FirstParent(ctx, root, main[0], 2)
	require.NoError(t, err)
	require.Len(t, history, 2)
	assert.Equal(t, main[0], history[0].SHA)
	assert.Equal(t, main[1], history[1].SHA)
	assert.NotEqual(t, history[0].Tree, history[1].Tree)

	tree, err := trees(ctx, pr)
	require.NoError(t, err)
	assert.Equal(t, history[1].Tree, tree, "the rebased head has the tree of the commit it merges as")
	assert.Empty(t, *asked)

	tree, err = trees(ctx, "abc")
	require.NoError(t, err)
	assert.Equal(t, "tree-abc", tree)
	assert.Equal(t, []string{"/repos/o/r/commits/abc"}, *asked)

	_, err = proven.FirstParent(ctx, root, "nope", 2)
	require.ErrorContains(t, err, "git log nope")
}
