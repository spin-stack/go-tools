package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/ghatrace"
)

// api answers as GitHub does for run 9 of o/r: two jobs, the artifacts each uploaded its spans
// in - one zipped, one a single file as it was - and an artifact that is not spans.
func api(t *testing.T) *httptest.Server {
	t.Helper()
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
		switch r.URL.Path {
		case "/repos/o/r/actions/runs/9/jobs":
			_, _ = w.Write([]byte(`{"jobs": [
				{"id": 1, "name": "plan", "workflow_name": "KVM", "status": "completed", "conclusion": "success", "runner_name": "r1",
				 "created_at": "2026-11-09T14:00:00Z", "started_at": "2026-11-09T14:00:02Z", "completed_at": "2026-11-09T14:01:00Z",
				 "steps": [{"name": "plan", "started_at": "2026-11-09T14:00:02Z", "completed_at": "2026-11-09T14:01:00Z"}]},
				{"id": 2, "name": "lane", "workflow_name": "KVM", "status": "completed", "conclusion": "failure", "runner_name": "r2",
				 "created_at": "2026-11-09T14:01:00Z", "started_at": "2026-11-09T14:01:05Z", "completed_at": "2026-11-09T14:05:00Z",
				 "steps": [{"name": "the lane", "conclusion": "failure", "started_at": "2026-11-09T14:01:06Z", "completed_at": "2026-11-09T14:05:00Z"}]}]}`))
		case "/repos/o/r/actions/runs/9/artifacts":
			body := `{"artifacts": [
				{"name": "ghatrace-lane", "archive_download_url": "http://` + r.Host + `/zip/1"},
				{"name": "ghatrace-plan.jsonl", "archive_download_url": "http://` + r.Host + `/raw/3"},
				{"name": "logs", "archive_download_url": "http://` + r.Host + `/zip/2"}]}`
			_, _ = w.Write([]byte(body)) //nolint:gosec // the test server names itself, as the API names where an artifact is
		case "/zip/1":
			_, _ = w.Write(archive.Bytes())
		case "/raw/3": // uploaded with archive: false
			_, _ = w.Write([]byte(`{"name": "decide", "runner": "r1", "start": "2026-11-09T14:00:10Z", "end": "2026-11-09T14:00:40Z"}` + "\n"))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
}

func TestRunWritesTheSummaryAndBothFiles(t *testing.T) {
	srv := api(t)
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
		require.NoError(t, run(t.Context(), []string{"run", "-run", "https://github.com/o/r/actions/runs/9/attempts/2", "-out", out, "-spans", local}))
	}

	got, err := os.ReadFile(summary)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(got), "an earlier step's\n## KVM: where the time went"))
	assert.Equal(t, 2, strings.Count(string(got), "## KVM"))
	assert.Contains(t, string(got), "**Critical path**: plan (58s, waited 2s) → lane (3m55s, waited 5s)")
	assert.Contains(t, string(got), "| e2e:up (span) | lane | 1m50s |", "from the zipped artifact")
	assert.Contains(t, string(got), "| decide (span) | plan | 30s |", "from the unarchived one")
	assert.Contains(t, string(got), "| teardown (span) | lane | 40s |", "from -spans")

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

func TestRunRefusesSpansItCannotRead(t *testing.T) {
	srv := api(t)
	defer srv.Close()
	t.Setenv("GITHUB_API_URL", srv.URL)
	t.Setenv("GH_TOKEN", "tok")
	t.Setenv("GITHUB_STEP_SUMMARY", "")
	dir := t.TempDir()
	assert.ErrorContains(t, run(t.Context(), []string{"run", "-repo", "o/r", "-run", "9", "-out", dir, "-spans", filepath.Join(dir, "missing")}), "no such file")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.jsonl"), []byte("{"), 0o600))
	assert.ErrorContains(t, run(t.Context(), []string{"run", "-repo", "o/r", "-run", "9", "-out", dir, "-spans", dir}), "bad.jsonl: line 1")
}

func TestCommands(t *testing.T) {
	for _, help := range []string{"help", "-h", "--help"} {
		assert.NoError(t, run(t.Context(), []string{help}), help)
	}
	assert.ErrorContains(t, run(t.Context(), nil), "a command is needed")
	assert.ErrorContains(t, run(t.Context(), []string{"draw"}), `no command "draw"`)
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

// stderr is what f wrote to os.Stderr.
func stderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	was := os.Stderr
	os.Stderr = w
	f()
	os.Stderr = was
	require.NoError(t, w.Close())
	said, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(said)
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
