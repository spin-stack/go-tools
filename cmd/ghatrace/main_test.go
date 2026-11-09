package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
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

	"github.com/spin-stack/go-tools/ghatrace"
)

// api answers as GitHub does for run 9 of o/r: two jobs that ran, one skipped and one still
// queued, the artifacts two uploaded their spans in - one zipped, one a single file as it was - and
// an artifact that is not spans. Each path asked goes to asked.
func api(t *testing.T, asked *[]string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	w, err := zw.Create("ghatrace-spans.jsonl")
	require.NoError(t, err)
	line, err := json.Marshal(ghatrace.Span{Name: "e2e:up", Runner: "r2", Start: time.Date(2026, 11, 9, 14, 1, 10, 0, time.UTC), End: time.Date(2026, 11, 9, 14, 3, 0, 0, time.UTC)})
	require.NoError(t, err)
	_, err = w.Write(append(line, '\n'))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
		mu.Lock()
		*asked = append(*asked, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/repos/o/r/actions/runs/9/jobs":
			_, _ = w.Write([]byte(`{"jobs": [
				{"id": 1, "name": "plan", "workflow_name": "KVM", "status": "completed", "conclusion": "success", "runner_name": "r1",
				 "created_at": "2026-11-09T14:00:00Z", "started_at": "2026-11-09T14:00:02Z", "completed_at": "2026-11-09T14:01:00Z",
				 "steps": [{"name": "plan", "started_at": "2026-11-09T14:00:02Z", "completed_at": "2026-11-09T14:01:00Z"}]},
				{"id": 2, "name": "lane", "workflow_name": "KVM", "status": "completed", "conclusion": "failure", "runner_name": "r2",
				 "created_at": "2026-11-09T14:01:00Z", "started_at": "2026-11-09T14:01:05Z", "completed_at": "2026-11-09T14:05:00Z",
				 "steps": [{"name": "the lane", "conclusion": "failure", "started_at": "2026-11-09T14:01:06Z", "completed_at": "2026-11-09T14:05:00Z"}]},
				{"id": 3, "name": "deploy", "workflow_name": "KVM", "status": "completed", "conclusion": "skipped",
				 "created_at": "2026-11-09T14:05:00Z", "started_at": "2026-11-09T14:05:00Z", "completed_at": "2026-11-09T14:05:00Z"},
				{"id": 4, "name": "notify", "workflow_name": "KVM", "status": "queued", "created_at": "2026-11-09T14:05:00Z"}]}`))
		case "/repos/o/r/actions/runs/9/artifacts":
			body := `{"artifacts": [
				{"name": "ghatrace-lane", "archive_download_url": "http://` + r.Host + `/zip/1"},
				{"name": "ghatrace-plan.jsonl", "archive_download_url": "http://` + r.Host + `/raw/3"},
				{"name": "logs", "archive_download_url": "http://` + r.Host + `/zip/2"}]}`
			_, _ = w.Write([]byte(body)) //nolint:gosec // the test server names itself, as the API names where an artifact is
		case "/zip/1":
			_, _ = w.Write(archive.Bytes())
		case "/repos/o/r/actions/jobs/1/logs": // the lane's is gone
			http.Redirect(w, r, "/blob/1", http.StatusFound)
		case "/blob/1":
			_, _ = w.Write([]byte("2026-11-09T14:00:02.1000000Z ##[group]Run plan\n" +
				"2026-11-09T14:00:05.0000000Z ##[group]Run fetch\n" +
				"2026-11-09T14:00:50.0000000Z ##[group]Run pick\n"))
		case "/raw/3": // uploaded with archive: false
			_, _ = w.Write([]byte(`{"name": "decide", "runner": "r1", "start": "2026-11-09T14:00:10Z", "end": "2026-11-09T14:00:40Z"}` + "\n"))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
}

func TestRunWritesTheSummaryAndBothFiles(t *testing.T) {
	var asked []string
	srv := api(t, &asked)
	defer srv.Close()
	dir := t.TempDir()
	summary := filepath.Join(dir, "summary.md")
	require.NoError(t, os.WriteFile(summary, []byte("an earlier step's\n"), 0o600))
	t.Setenv("GITHUB_API_URL", srv.URL)
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("GITHUB_STEP_SUMMARY", summary)
	t.Setenv("GITHUB_SERVER_URL", "https://github.example")
	out := filepath.Join(dir, "out")
	// Spans from a directory too: a .jsonl file anywhere under it, and nothing else.
	local := filepath.Join(dir, "spans")
	require.NoError(t, os.MkdirAll(filepath.Join(local, "lane"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(local, "lane", "s.jsonl"), []byte(`{"name": "teardown", "runner": "r2", "start": "2026-11-09T14:04:00Z", "end": "2026-11-09T14:04:40Z"}`+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(local, "notes.txt"), []byte("not spans"), 0o600))

	// Twice: a rerun's files are its own, and the summary keeps what each step added.
	for range 2 {
		said := stderr(t, func() {
			require.NoError(t, run(t.Context(), []string{"run", "-run", "https://github.com/o/r/actions/runs/9/attempts/2", "-out", out, "-spans", local}))
		})
		assert.Contains(t, said, "ghatrace: lane: no steps of its steps' own", "a log that fails to come leaves the job as the API has it")
	}

	got, err := os.ReadFile(summary)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(got), "an earlier step's\n## KVM: where the time went"))
	assert.Equal(t, 2, strings.Count(string(got), "## KVM"))
	assert.Contains(t, string(got), "**Critical path**: plan (58s, waited 2s) → lane (3m55s, waited 5s)")
	assert.Contains(t, string(got), "| e2e:up (span) | lane | 1m50s |", "from the zipped artifact")
	assert.Contains(t, string(got), "| decide (span) | plan | 30s |", "from the unarchived one")
	assert.Contains(t, string(got), "| teardown (span) | lane | 40s |", "from -spans")
	assert.Contains(t, string(got), "| pick | plan | 11s |", "from the plan's log, the last part to the step's end, timed to the second")
	var logs []string
	for _, p := range asked {
		if strings.HasSuffix(p, "/logs") {
			logs = append(logs, p)
		}
	}
	assert.Equal(t, []string{"/repos/o/r/actions/jobs/1/logs", "/repos/o/r/actions/jobs/2/logs"}, logs[:2],
		"the logs of the jobs that ran, not of one skipped or one still queued")
	assert.Len(t, logs, 4, "twice")

	page, err := os.ReadFile(filepath.Join(out, "trace.html"))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(page), "<!doctype html>"))
	assert.Contains(t, string(page), `href="https://github.example/o/r/actions/runs/9"`)
	var trace struct {
		Events []json.RawMessage `json:"traceEvents"`
	}
	raw, err := os.ReadFile(filepath.Join(out, "trace.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &trace))
	assert.Contains(t, string(raw), `"e2e:up 1m50s"`)
}

func TestRunOf(t *testing.T) {
	for _, c := range []struct {
		id, repo string
		run      int64
		owner    string
		err      string
	}{
		{id: "9", repo: "o/r", run: 9, owner: "o/r"},
		{id: "https://github.com/a/b/actions/runs/42", repo: "o/r", run: 42, owner: "a/b"},
		{id: "https://github.com/a/b/actions/runs/42/attempts/3", run: 42, owner: "a/b"},
		{id: "a/b/actions/runs/5", run: 5, owner: "a/b"},
		{id: "", repo: "o/r", err: "-run or GITHUB_RUN_ID"},
		{id: "9a", repo: "o/r", err: "-run or GITHUB_RUN_ID"},
		{id: "9", err: "-repo or GITHUB_REPOSITORY"},
	} {
		run, owner, err := runOf(c.id, c.repo)
		if c.err != "" {
			assert.ErrorContains(t, err, c.err, c.id)
			continue
		}
		require.NoError(t, err, c.id)
		assert.Equal(t, c.run, run, c.id)
		assert.Equal(t, c.owner, owner, c.id)
	}
}

func TestRunWithoutLogsAsksForNone(t *testing.T) {
	var asked []string
	srv := api(t, &asked)
	defer srv.Close()
	t.Setenv("GITHUB_API_URL", srv.URL)
	t.Setenv("GH_TOKEN", "tok")
	t.Setenv("GITHUB_STEP_SUMMARY", "")
	out := written(t, &os.Stdout, func() {
		require.NoError(t, run(t.Context(), []string{"run", "-repo", "o/r", "-run", "9", "-out", t.TempDir(), "-logs=false"}))
	})
	assert.NotContains(t, out, "| pick |")
	for _, p := range asked {
		assert.NotContains(t, p, "/logs")
	}
}

func TestRunRefusesSpansItCannotRead(t *testing.T) {
	var asked []string
	srv := api(t, &asked)
	defer srv.Close()
	t.Setenv("GITHUB_API_URL", srv.URL)
	t.Setenv("GH_TOKEN", "tok")
	t.Setenv("GITHUB_STEP_SUMMARY", "")
	dir := t.TempDir()
	assert.ErrorContains(t, run(t.Context(), []string{"run", "-repo", "o/r", "-run", "9", "-out", dir, "-spans", filepath.Join(dir, "missing")}), "no such file")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.jsonl"), []byte("{"), 0o600))
	assert.ErrorContains(t, run(t.Context(), []string{"run", "-repo", "o/r", "-run", "9", "-out", dir, "-spans", dir}), "bad.jsonl: line 1")
}

// -json goes right after go test's own `go test`, and into nothing else.
func TestWithJSON(t *testing.T) {
	for _, c := range []struct{ in, want []string }{
		{[]string{"go", "test", "-v", "./..."}, []string{"go", "test", "-json", "-v", "./..."}},
		{[]string{"/usr/local/go/bin/go", "test"}, []string{"/usr/local/go/bin/go", "test", "-json"}},
		{[]string{"make", "test"}, []string{"make", "test"}},
		{[]string{"sh", "-c", "go test -json ."}, []string{"sh", "-c", "go test -json ."}},
	} {
		assert.Equal(t, c.want, withJSON(c.in), strings.Join(c.in, " "))
	}
}

func TestCommands(t *testing.T) {
	for _, help := range []string{"help", "-h", "--help"} {
		assert.NoError(t, run(t.Context(), []string{help}), help)
	}
	assert.ErrorContains(t, run(t.Context(), nil), "a command is needed")
	assert.ErrorContains(t, run(t.Context(), []string{"draw"}), `no command "draw"`)
	assert.ErrorContains(t, run(t.Context(), []string{"span"}), "a command is needed after --")
	assert.ErrorContains(t, run(t.Context(), []string{"gotest", "-min", "1s"}), "a go test command is needed after --")
}

// A test's span that cannot be written says so, and go test's answer is still the command's.
func TestGoTestSpanThatCannotBeRecordedSaysSo(t *testing.T) {
	t.Setenv("GHATRACE_SPANS", filepath.Join(t.TempDir(), "missing", "spans.jsonl"))
	t.Setenv("GOFLAGS", "-count=1")
	t.Setenv("SAMPLE_FAIL", "")
	t.Chdir(filepath.Join("testdata", "sample"))
	var err error
	said := stderr(t, func() {
		_ = written(t, &os.Stdout, func() { err = run(t.Context(), []string{"gotest", "--", "go", "test", "."}) })
	})
	require.NoError(t, err)
	assert.Contains(t, said, `ghatrace: span "example.com/sample" not recorded`)
}

func TestSpanRecordsTheCommandAndExitsAsItDid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spans.jsonl")
	t.Setenv("GHATRACE_SPANS", path)
	t.Setenv("RUNNER_NAME", "r7")
	t.Setenv("GITHUB_JOB", "lane")

	err := run(t.Context(), []string{"span", "-name", "seed", "--", "sh", "-c", "exit 3"})
	var exit exitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, "exit status 3", exit.Error())
	require.NoError(t, run(t.Context(), []string{"span", "--", "true"}))

	f, err := os.Open(path) //nolint:gosec // the test's own file
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	spans, err := ghatrace.Read(f)
	require.NoError(t, err)
	require.Len(t, spans, 2)
	assert.Equal(t, "seed", spans[0].Name)
	assert.Equal(t, 3, spans[0].Exit)
	assert.Equal(t, "r7", spans[0].Runner)
	assert.Equal(t, "lane", spans[0].Job)
	assert.False(t, spans[0].End.Before(spans[0].Start))
	assert.Equal(t, "true", spans[1].Name, "the command names a span with no -name")
}

// A command a signal killed - a cancelled job's - is a failure, recorded as the shell records it.
func TestSpanOfACommandKilled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spans.jsonl")
	t.Setenv("GHATRACE_SPANS", path)
	err := run(t.Context(), []string{"span", "--", "sh", "-c", "kill -KILL $$"})
	assert.Equal(t, exitError(128), err)
	raw, err := os.ReadFile(path) //nolint:gosec // the test's own file
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"exit":128`)
}

// written is what f wrote to *to, os.Stdout or os.Stderr.
func written(t *testing.T, to **os.File, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	was := *to
	*to = w
	f()
	*to = was
	require.NoError(t, w.Close())
	said, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(said)
}

func stderr(t *testing.T, f func()) string { t.Helper(); return written(t, &os.Stderr, f) }

// gotest runs a real go test, in testdata/sample: what it writes is what go test would have, it
// exits as go test did, and the package and its tests are spans under the command's.
func TestGoTestWritesWhatGoTestWouldAndRecordsItsTests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spans.jsonl")
	t.Setenv("GHATRACE_SPANS", path)
	t.Setenv("GOFLAGS", "-count=1")
	t.Chdir(filepath.Join("testdata", "sample"))

	t.Setenv("SAMPLE_FAIL", "1")
	var err error
	out := written(t, &os.Stdout, func() { err = run(t.Context(), []string{"gotest", "-min", "0", "--", "go", "test", "."}) })
	var exit exitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, exitError(1), exit)
	assert.Contains(t, out, "--- FAIL: TestFailsWhenAsked")
	assert.NotContains(t, out, "said only with -v", "a passing test says nothing without -v")
	assert.NotContains(t, out, `"Action"`, "no JSON reaches the log")

	// A wrapper in front of go test, whose own lines - an empty one among them - are written as
	// they are, and whose go test writes JSON all the same.
	t.Setenv("SAMPLE_FAIL", "")
	out = written(t, &os.Stdout, func() {
		err = run(t.Context(), []string{"gotest", "-name", "unit", "--", "sh", "-c", "echo; echo wrapper; exec go test -json -v ."})
	})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(out, "\nwrapper\n"), out)
	assert.Contains(t, out, "said only with -v")
	assert.NotContains(t, out, `"Action"`)

	f, err := os.Open(path) //nolint:gosec // the test's own file
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	spans, err := ghatrace.Read(f)
	require.NoError(t, err)
	var got []string
	for _, s := range spans {
		got = append(got, fmt.Sprintf("%s<%s>%d", s.Name, s.Parent, s.Exit))
	}
	assert.Equal(t, []string{
		"TestPasses<example.com/sample>0", "TestSays<example.com/sample>0", "TestFailsWhenAsked<example.com/sample>1",
		"TestGoCommandsItRunsWriteNoJSON<example.com/sample>0",
		"example.com/sample<go test>1", "go test<>1",
		// -min 1s: no test of the second run is a span; its package is.
		"example.com/sample<unit>0", "unit<>0",
	}, got)
}

// Recording is for the reader of the run: a span that cannot be written says so and does not
// fail the step, and one that is written says nothing.
func TestSpanThatCannotBeRecordedSaysSo(t *testing.T) {
	dir := t.TempDir()
	for file, want := range map[string]string{
		filepath.Join(dir, "missing", "spans.jsonl"): `ghatrace: span "ok" not recorded`,
		filepath.Join(dir, "spans.jsonl"):            "",
	} {
		t.Setenv("GHATRACE_SPANS", file)
		var err error
		said := stderr(t, func() { err = run(t.Context(), []string{"span", "-name", "ok", "--", "true"}) })
		require.NoError(t, err)
		if want == "" {
			assert.Empty(t, said)
		} else {
			assert.Contains(t, said, want)
		}
	}
}

func TestSpanOffARunnerOnlyRunsTheCommand(t *testing.T) {
	t.Setenv("GHATRACE_SPANS", "")
	t.Setenv("RUNNER_TEMP", "")
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, run(t.Context(), []string{"span", "--", "sh", "-c", "touch ran"}))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "ran", entries[0].Name())
}
