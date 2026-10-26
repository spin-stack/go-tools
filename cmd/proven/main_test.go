package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/internal/proven"
)

// stdout is what f printed to os.Stdout.
func stdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	was := os.Stdout
	os.Stdout = w
	f()
	os.Stdout = was
	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

type ciRun struct {
	ID         int64  `json:"id"`
	HeadSHA    string `json:"head_sha"`
	HeadBranch string `json:"head_branch"`
	Event      string `json:"event"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"html_url"`
}

// fixture is a clone of main with three commits and a pull request's head that has the second's
// tree, and an API whose ci.yml passed on that head (on a dispatch) and, older, on the first
// commit; whose kvm.yml is as the test says; and whose one e2e-verify job logged two tests. The
// command is run in the clone, as the workflow's environment has it.
type fixture struct {
	main    []string // main, main~1, main~2
	pr      string
	kvm     []ciRun
	asked   []string
	api     string // the test server
	archive []byte // run 6's artifact, a zip of a lane's logs
}

func setup(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	for _, name := range []string{"one", "two", "three"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(name), 0o600))
		git("add", name)
		git("commit", "-q", "-m", name)
	}
	git("checkout", "-q", "-b", "pr", "HEAD~1")
	git("commit", "-q", "--allow-empty", "-m", "rebased")
	git("checkout", "-q", "main")
	f := &fixture{main: []string{git("rev-parse", "main"), git("rev-parse", "main~1"), git("rev-parse", "main~2")}, pr: git("rev-parse", "pr")}
	t.Chdir(root)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.asked = append(f.asked, r.URL.RequestURI())
		assert.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/repos/o/r/actions/workflows/ci.yml/runs":
			// As the API filters: status=success is the passed runs, status=completed every finished one.
			var runs []ciRun
			for _, run := range []ciRun{
				{ID: 6, HeadSHA: f.main[0], HeadBranch: "main", Event: "schedule", Status: "completed", Conclusion: "failure", URL: "u6"},
				{ID: 5, HeadSHA: "elsewhere", Event: "schedule", Status: "completed", Conclusion: "cancelled", URL: "u5"},
				{ID: 4, HeadSHA: f.pr, Event: "workflow_dispatch", Status: "completed", Conclusion: "success", URL: "u4"},
				{ID: 3, HeadSHA: f.main[2], Event: "schedule", Status: "completed", Conclusion: "success", URL: "u3"},
			} {
				if r.URL.Query().Get("status") != "success" || run.Conclusion == "success" {
					runs = append(runs, run)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"workflow_runs": runs})
		case "/repos/o/r/actions/workflows/kvm.yml/runs":
			_ = json.NewEncoder(w).Encode(map[string]any{"workflow_runs": f.kvm})
		case "/repos/o/r/actions/runs/4/jobs":
			_, _ = w.Write([]byte(`{"jobs": [{"id": 41, "name": "short"}, {"id": 42, "name": "e2e-verify:1/1", "html_url": "j42", "labels": ["runs-on/fleet=kvm"]}]}`))
		case "/repos/o/r/actions/runs/6/jobs":
			_, _ = w.Write([]byte(`{"jobs": []}`))
		case "/repos/o/r/actions/runs/3/jobs":
			_, _ = w.Write([]byte(`{"jobs": [{"id": 31, "name": "e2e-verify:1/1"}]}`))
		case "/repos/o/r/actions/jobs/42/logs":
			_, _ = w.Write([]byte("--- PASS: TestA (1.50s)\n"))
		case "/repos/o/r/actions/jobs/31/logs":
			_, _ = w.Write([]byte("--- PASS: TestA (1.40s)\n--- FAIL: TestB (2s)\n"))
		default:
			if f.flakesAPI(w, r) {
				return
			}
			// A commit the clone has not: its tree is never the one asked about.
			if strings.HasPrefix(r.URL.Path, "/repos/o/r/commits/") {
				_, _ = w.Write([]byte(`{"commit": {"tree": {"sha": "other"}}}`))
				return
			}
			http.Error(w, "not here", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	f.api = srv.URL
	t.Setenv("GITHUB_API_URL", srv.URL)
	t.Setenv("GITHUB_REPOSITORY", "o/r")
	t.Setenv("GH_TOKEN", "tok")
	t.Setenv("GITHUB_OUTPUT", "")
	t.Setenv("GITHUB_STEP_SUMMARY", "")
	t.Setenv("GITHUB_SHA", "")
	t.Setenv("GITHUB_REF", "")
	return f
}

// flakesAPI answers what flakes asks: run 6 (ci.yml, failed) kept a lane's logs as an artifact;
// kvm's failed runs kept none, and their failed jobs' logs are read instead. False for anything
// else.
func (f *fixture) flakesAPI(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/repos/o/r/actions/runs/6/artifacts":
		_, _ = w.Write([]byte(`{"artifacts": [{"name": "ci-lanes-short-6", "archive_download_url": "` + f.api + `/archive/6", "expired": false}, {"name": "old", "archive_download_url": "` + f.api + `/archive/none", "expired": true}, {"name": "deleted", "archive_download_url": "` + f.api + `/archive/gone", "expired": false}]}`))
	case "/archive/gone":
		http.Error(w, "gone", http.StatusGone)
	case "/archive/6":
		_, _ = w.Write(f.archive)
	case "/repos/o/r/actions/runs/30/artifacts", "/repos/o/r/actions/runs/33/artifacts", "/repos/o/r/actions/runs/35/artifacts", "/repos/o/r/actions/runs/36/artifacts":
		_, _ = w.Write([]byte(`{"artifacts": []}`))
	case "/repos/o/r/actions/runs/35/jobs":
		_, _ = w.Write([]byte(`{"jobs": [{"id": 37, "name": "Root lanes", "conclusion": "failure"}]}`))
	case "/repos/o/r/actions/runs/36/jobs":
		// Job 40's log is gone, as last week's are: it says nothing, and 38's is still read.
		_, _ = w.Write([]byte(`{"jobs": [{"id": 40, "name": "Seal", "conclusion": "failure"}, {"id": 38, "name": "Root lanes", "conclusion": "failure"}]}`))
	case "/repos/o/r/actions/jobs/40/logs":
		http.Error(w, "BlobNotFound", http.StatusNotFound)
	case "/repos/o/r/actions/jobs/37/logs":
		_, _ = w.Write([]byte("--- FAIL: TestD (3s)\n"))
	case "/repos/o/r/actions/jobs/38/logs":
		_, _ = w.Write([]byte("--- FAIL: TestD (3s)\n--- FAIL: TestC (1s)\n"))
	case "/repos/o/r/actions/runs/30/jobs":
		_, _ = w.Write([]byte(`{"jobs": [{"id": 31, "name": "e2e-verify:1/1", "conclusion": "failure"}, {"id": 39, "name": "short", "conclusion": "success"}]}`))
	case "/repos/o/r/actions/runs/33/jobs":
		_, _ = w.Write([]byte(`{"jobs": [{"id": 34, "name": "e2e-verify:1/1", "conclusion": "failure"}]}`))
	case "/repos/o/r/actions/jobs/34/logs":
		_, _ = w.Write([]byte("kvm\tRoot lanes\t2026-10-25T12:00:00Z --- FAIL: TestB (3s)\n--- FAIL: TestE (1s)\n"))
	default:
		return false
	}
	return true
}

// tree answers for the commit's tree: main~1 is proven by the pull request's run on the same tree,
// main by nothing, and the answer is proven=true|false either way, written to the workflow's
// output when there is one and said when there is not.
func TestTreeAnswersForTheTree(t *testing.T) {
	f := setup(t)
	var err error
	out := stdout(t, func() { err = run(t.Context(), []string{"tree", "-sha", f.main[1]}) })
	require.NoError(t, err)
	assert.Equal(t, "ci.yml passed on "+f.main[1]+"'s tree: u4: what that run ran is not run again\nproven=true\n", out)

	out = stdout(t, func() { err = run(t.Context(), []string{"tree", "-sha", f.main[0]}) })
	require.NoError(t, err)
	assert.Equal(t, "ci.yml did not pass on "+f.main[0]+"'s tree (failure): u6: every lane\nproven=false\n", out)

	out = stdout(t, func() { err = run(t.Context(), []string{"tree", "-sha", f.main[0], "-events", "push"}) })
	require.NoError(t, err)
	assert.Equal(t, "ci.yml never finished on "+f.main[0]+"'s tree: every lane\nproven=false\n", out)

	output := filepath.Join(t.TempDir(), "output")
	summary := filepath.Join(t.TempDir(), "summary")
	t.Setenv("GITHUB_OUTPUT", output)
	t.Setenv("GITHUB_STEP_SUMMARY", summary)
	t.Setenv("GITHUB_SHA", f.pr)
	out = stdout(t, func() { err = run(t.Context(), []string{"tree"}) })
	require.NoError(t, err)
	assert.Equal(t, "ci.yml passed on "+f.pr+"'s tree: u4: what that run ran is not run again\n", out)
	written, err := os.ReadFile(output)
	require.NoError(t, err)
	assert.Equal(t, "proven=true\n", string(written))
	written, err = os.ReadFile(summary)
	require.NoError(t, err)
	assert.Equal(t, "ci.yml passed on "+f.pr+"'s tree: u4: what that run ran is not run again\n", string(written))
}

// base is what a run on main is of: main~1 is proven, by the pull request's run on its tree; main
// is new since main~1, which is its base - and not main~2, where the last run on main itself
// passed (spin#659); a run not on main is of its branch against main. A main whose history has
// no commit any run passed on is of its last commit; one with no commit before is an error.
func TestBaseIsWhatMainBecameSinceARunPassed(t *testing.T) {
	f := setup(t)
	t.Setenv("GITHUB_REF", "refs/heads/main")
	var err error
	out := stdout(t, func() { err = run(t.Context(), []string{"base", "-sha", f.main[0]}) })
	require.NoError(t, err)
	assert.Equal(t, "main moved since "+f.main[1]+" (u4); every lane, mutate against it\nbase="+f.main[1]+"\n", out)

	out = stdout(t, func() { err = run(t.Context(), []string{"base", "-sha", f.main[1]}) })
	require.NoError(t, err)
	assert.Equal(t, "ci.yml passed on main's tree: u4\nproven=true\n", out)

	out = stdout(t, func() { err = run(t.Context(), []string{"base", "-sha", f.main[0], "-events", "push"}) })
	require.NoError(t, err)
	assert.Equal(t, "none of main's last 3 commits passed in the last 40 runs: the last commit is what is new\nbase="+f.main[1]+"\n", out)

	out = stdout(t, func() { err = run(t.Context(), []string{"base", "-sha", f.main[0], "-events", "push", "-depth", "2"}) })
	require.NoError(t, err)
	assert.Equal(t, "none of main's last 2 commits passed in the last 40 runs: the last commit is what is new\nbase="+f.main[1]+"\n", out)

	out = stdout(t, func() { err = run(t.Context(), []string{"base", "-sha", f.main[2], "-events", "push"}) })
	require.ErrorContains(t, err, "no commit before")
	assert.Empty(t, out)

	out = stdout(t, func() { err = run(t.Context(), []string{"base", "-sha", f.main[0], "-ref", "refs/heads/fix"}) })
	require.NoError(t, err)
	assert.Equal(t, "a dispatch on refs/heads/fix: every lane, mutate against main\nbase=origin/main\n", out)
}

// release refuses unless ci.yml passed on the tree and kvm.yml's newest run on the commit passed,
// and says each answer; the commit may be given short; -also "" asks only the gate.
func TestReleaseIsRefusedUntilBothPassed(t *testing.T) {
	f := setup(t)
	var err error
	out := stdout(t, func() { err = run(t.Context(), []string{"release", "-sha", f.main[1]}) })
	require.ErrorIs(t, err, errRefused)
	assert.Equal(t, "ci.yml passed on "+f.main[1]+"'s tree: u4\n::error::kvm.yml never ran on "+f.main[1]+" from a push to main: a release is made from a commit main tested\n", out)

	f.kvm = []ciRun{{ID: 9, HeadSHA: f.main[1], Event: "push", Status: "completed", Conclusion: "success", URL: "k9"}}
	// By its short sha too: the API's head_sha filter wants the whole of it.
	out = stdout(t, func() { err = run(t.Context(), []string{"release", "-sha", f.main[1][:9]}) })
	require.NoError(t, err)
	assert.Equal(t, "ci.yml passed on "+f.main[1]+"'s tree: u4\nkvm.yml passed on "+f.main[1]+": k9\n", out)
	assert.Contains(t, f.asked, "/repos/o/r/actions/workflows/kvm.yml/runs?head_sha="+f.main[1]+"&per_page=40")

	f.kvm = []ciRun{{ID: 9, HeadSHA: f.main[1], Event: "pull_request", Status: "completed", Conclusion: "success", URL: "k9"}}
	out = stdout(t, func() { err = run(t.Context(), []string{"release", "-sha", f.main[1]}) })
	require.ErrorIs(t, err, errRefused)
	assert.Contains(t, out, "kvm.yml never ran")

	out = stdout(t, func() { err = run(t.Context(), []string{"release", "-sha", f.main[0], "-also", ""}) })
	require.ErrorIs(t, err, errRefused)
	assert.Equal(t, "::error::ci.yml did not pass on "+f.main[0]+"'s tree (failure): u6\n", out)
}

// times is each test's time from the newest run that passed whose e2e-verify jobs logged any -
// run 4's one, not run 3's two; with no such run, none.
func TestTimesAreTheLastGatesThatLoggedAny(t *testing.T) {
	f := setup(t)
	output := filepath.Join(t.TempDir(), "output")
	t.Setenv("GITHUB_OUTPUT", output)
	var err error
	out := stdout(t, func() { err = run(t.Context(), []string{"times"}) })
	require.NoError(t, err)
	assert.Equal(t, "run 4: 1 tests' times\n", out)
	written, err := os.ReadFile(output)
	require.NoError(t, err)
	assert.Equal(t, "times=1.50 TestA\n", string(written))
	assert.Contains(t, f.asked, "/repos/o/r/actions/workflows/ci.yml/runs?per_page=40&status=success")

	t.Setenv("GITHUB_OUTPUT", "")
	out = stdout(t, func() { err = run(t.Context(), []string{"times", "-jobs", "none"}) })
	require.NoError(t, err)
	assert.Equal(t, "run none: 0 tests' times\ntimes=\n", out)
}

// tests: main~1 is answered for by the pull request's run 4 (its tree) and kvm's run 30 (its
// sha); every test their jobs logged is in the record with its job, a job whose log is gone says
// nothing, and the record goes to -out or stdout.
func TestTestsAreWhatTheAnsweringRunsLogged(t *testing.T) {
	f := setup(t)
	// A re-run still going is not an answer yet; the newest that finished is.
	f.kvm = []ciRun{
		{ID: 33, HeadSHA: f.main[1], HeadBranch: "main", Event: "push", Status: "in_progress", URL: "u33"},
		{ID: 30, HeadSHA: f.main[1], HeadBranch: "main", Event: "push", Status: "completed", Conclusion: "failure", URL: "u30"},
	}
	out := filepath.Join(t.TempDir(), "tests.json")
	var err error
	said := stdout(t, func() { err = run(t.Context(), []string{"tests", "-sha", f.main[1], "-out", out}) })
	require.NoError(t, err)
	assert.Equal(t, "2 runs answer for "+f.main[1]+": 3 tests logged\n", said)
	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	var got evidence
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, evidence{
		Commit: f.main[1],
		Runs: []evidenceRun{
			{Workflow: "ci.yml", ID: 4, URL: "u4", Conclusion: "success", Head: f.pr},
			{Workflow: "kvm.yml", ID: 30, URL: "u30", Conclusion: "failure", Head: f.main[1]},
		},
		Tests: []evidenceOf{
			{Test: proven.Test{Name: "TestA", Passed: true, Seconds: 1.5}, Workflow: "ci.yml", Run: 4, Job: "e2e-verify:1/1", JobURL: "j42", Runner: []string{"runs-on/fleet=kvm"}},
			{Test: proven.Test{Name: "TestA", Passed: true, Seconds: 1.4}, Workflow: "kvm.yml", Run: 30, Job: "e2e-verify:1/1"},
			{Test: proven.Test{Name: "TestB", Passed: false, Seconds: 2}, Workflow: "kvm.yml", Run: 30, Job: "e2e-verify:1/1"},
		},
	}, got)
	assert.Contains(t, f.asked, "/repos/o/r/actions/jobs/41/logs", "a job whose log is gone is asked, and says nothing")

	// main's head: the gate's run 6 failed on its tree and logged nothing, and kvm never ran on
	// it - a record of that run and no test, to stdout.
	f.kvm = nil
	said = stdout(t, func() { err = run(t.Context(), []string{"tests", "-sha", f.main[0]}) })
	require.NoError(t, err)
	want, err := json.MarshalIndent(evidence{Commit: f.main[0], Runs: []evidenceRun{{Workflow: "ci.yml", ID: 6, URL: "u6", Conclusion: "failure", Head: f.main[0]}}, Tests: []evidenceOf{}}, "", "  ")
	require.NoError(t, err)
	assert.Equal(t, "1 runs answer for "+f.main[0]+": 0 tests logged\n"+string(want)+"\n", said)
}

// flakes: run 6 failed TestA on main's tree, which no other run has - red; kvm's TestB failed on
// two runs of main~1's tree and passed on a third - flaky, and a quarantine candidate. The table
// goes to the summary when there is one.
func TestFlakesAreTheTestsThatPassedOnTheSameTree(t *testing.T) {
	f := setup(t)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"_output/ci-local/short.log": "--- FAIL: TestA (1s)\n", "_output/ci-local/failed": "test\n"} {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	f.archive = buf.Bytes()
	f.kvm = []ciRun{
		{ID: 36, HeadSHA: f.main[2], HeadBranch: "main", Event: "push", Status: "completed", Conclusion: "failure", URL: "u36"},
		{ID: 35, HeadSHA: f.main[2], HeadBranch: "main", Event: "push", Status: "completed", Conclusion: "failure", URL: "u35"},
		{ID: 33, HeadSHA: f.main[1], HeadBranch: "main", Event: "push", Status: "completed", Conclusion: "failure", URL: "u33"},
		{ID: 32, HeadSHA: f.main[1], HeadBranch: "main", Event: "push", Status: "completed", Conclusion: "success", URL: "u32"},
		{ID: 30, HeadSHA: f.main[1], HeadBranch: "main", Event: "push", Status: "completed", Conclusion: "failure", URL: "u30"},
	}
	summaryFile := filepath.Join(t.TempDir(), "summary")
	t.Setenv("GITHUB_STEP_SUMMARY", summaryFile)
	var err error
	out := stdout(t, func() { err = run(t.Context(), []string{"flakes"}) })
	require.NoError(t, err)
	// TestD failed twice in a row on main's tree: red, and no candidate however many times; TestE
	// failed once beside a pass: flaky, and no candidate for once; TestA failed once with nothing
	// else on its tree: once.
	assert.Equal(t, "| test | failed | of runs | branches | verdict |\n| --- | --- | --- | --- | --- |\n"+
		"| kvm.yml: TestB | 2 ([33](u33) [30](u30)) | 9 | main | flaky: did not fail on another run of the same tree |\n"+
		"| kvm.yml: TestD | 2 ([36](u36) [35](u35)) | 9 | main | red: failed on runs in a row |\n"+
		"| ci.yml: TestA | 1 ([6](u6)) | 9 | main | once: no other run of its tree to tell |\n"+
		"| kvm.yml: TestC | 1 ([36](u36)) | 9 | main | flaky: did not fail on another run of the same tree |\n"+
		"| kvm.yml: TestE | 1 ([33](u33)) | 9 | main | flaky: did not fail on another run of the same tree |\n"+
		"flaky more than once, for hack/quarantine.txt or a fix: kvm.yml: TestB\n", out)
	written, err := os.ReadFile(summaryFile)
	require.NoError(t, err)
	assert.Equal(t, 7, strings.Count(string(written), "\n"))
	assert.Contains(t, f.asked, "/repos/o/r/actions/runs/6/artifacts?per_page=100")
	assert.NotContains(t, strings.Join(f.asked, " "), "/archive/none", "an expired artifact is not fetched")
	assert.Contains(t, f.asked, "/archive/gone", "one the API no longer holds is asked for, and says nothing")
	assert.Contains(t, f.asked, "/repos/o/r/actions/jobs/40/logs")
	assert.Contains(t, f.asked, "/repos/o/r/actions/jobs/34/logs")
	assert.NotContains(t, f.asked, "/repos/o/r/actions/jobs/39/logs", "a job that passed has no failure to read")
	assert.NotContains(t, strings.Join(f.asked, " "), "/runs/6/jobs", "a run whose artifact named its failures is not asked for its jobs")

	// Nothing flaky more than once: said so. -also empty asks one workflow.
	f.kvm = nil
	out = stdout(t, func() { err = run(t.Context(), []string{"flakes", "-also", ""}) })
	require.NoError(t, err)
	assert.Contains(t, out, "1 failed tests in 4 runs; none flaky more than once\n")

	// An artifact that is not a zip is said with its name.
	f.archive = []byte("nope")
	err = run(t.Context(), []string{"flakes", "-also", ""})
	assert.ErrorContains(t, err, "run 6: artifact ci-lanes-short-6: the artifact is not a zip")
}

// What is refused before the API is asked: no command, an unknown one, a flag it has not, no
// repository; help is said for the asking. HEAD is the commit when none is named.
func TestWhatIsRefusedBeforeAsking(t *testing.T) {
	f := setup(t)
	for name, tc := range map[string]struct {
		argv  []string
		fails string
		out   string
	}{
		"nothing":         {fails: "a command is needed", out: usage},
		"help":            {argv: []string{"help"}, out: usage},
		"-h":              {argv: []string{"-h"}, out: usage},
		"an unknown one":  {argv: []string{"frob"}, fails: `no command "frob"`},
		"an unknown flag": {argv: []string{"tree", "-nope"}, fails: "flag provided but not defined"},
	} {
		t.Run(name, func(t *testing.T) {
			var err error
			out := stdout(t, func() { err = run(t.Context(), tc.argv) })
			if tc.fails == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.fails)
			}
			assert.Equal(t, tc.out, out)
		})
	}
	t.Setenv("GITHUB_REPOSITORY", "")
	err := run(t.Context(), []string{"tree"})
	require.ErrorContains(t, err, "GITHUB_REPOSITORY")

	t.Setenv("GITHUB_REPOSITORY", "o/r")
	out := stdout(t, func() { err = run(t.Context(), []string{"tree"}) })
	require.NoError(t, err)
	assert.Contains(t, out, f.main[0]+"'s tree", "HEAD is main")

	// A commit the clone has not is asked of the API as given; HEAD outside a clone is nobody's.
	out = stdout(t, func() { err = run(t.Context(), []string{"tree", "-sha", "deadbeef"}) })
	require.NoError(t, err)
	assert.Equal(t, "ci.yml never finished on deadbeef's tree: every lane\nproven=false\n", out)
	t.Chdir(t.TempDir())
	err = run(t.Context(), []string{"tree"})
	require.ErrorContains(t, err, "HEAD")
}

// The token is GH_TOKEN, else GITHUB_TOKEN.
func TestTheTokenIsEitherOfTheTwo(t *testing.T) {
	f := setup(t)
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "tok")
	var err error
	out := stdout(t, func() { err = run(t.Context(), []string{"tree", "-sha", f.main[1]}) })
	require.NoError(t, err)
	assert.Contains(t, out, "proven=true")
}

// An output with lines is a heredoc, one without is name=value; a file that cannot be written is
// an error, since the answer is the output.
func TestAnOutputReachesTheWorkflow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output")
	t.Setenv("GITHUB_OUTPUT", path)
	require.NoError(t, output("a", "1"))
	require.NoError(t, output("b", "x\ny"))
	written, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "a=1\nb<<EOF\nx\ny\nEOF\n", string(written))

	t.Setenv("GITHUB_OUTPUT", "/dev/full")
	require.Error(t, output("a", "1"))
	t.Setenv("GITHUB_OUTPUT", filepath.Join(t.TempDir(), "none", "output"))
	require.Error(t, output("a", "1"))
}
