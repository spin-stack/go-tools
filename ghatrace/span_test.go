package ghatrace_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/ghatrace"
)

func TestRecordedSpansReadBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spans.jsonl")
	t.Setenv("GHATRACE_SPANS", path)
	t.Setenv("RUNNER_NAME", "r7")
	t.Setenv("GITHUB_JOB", "lane")
	start := time.Date(2026, 11, 9, 14, 0, 0, 123, time.UTC)
	require.NoError(t, ghatrace.Record(ghatrace.Span{Name: "a", Start: start, End: start.Add(time.Second)}))
	require.NoError(t, ghatrace.Record(ghatrace.Span{Name: "b", Start: start, End: start, Exit: 2, Runner: "other", Job: "job"}))

	f, err := os.Open(path) //nolint:gosec // the test's own file
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	got, err := ghatrace.Read(f)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.True(t, start.Equal(got[0].Start), "to the nanosecond")
	assert.Equal(t, []string{"r7", "lane"}, []string{got[0].Runner, got[0].Job}, "this job's, when the span names none")
	assert.Equal(t, []string{"other", "job", "b"}, []string{got[1].Runner, got[1].Job, got[1].Name})
	assert.Equal(t, 2, got[1].Exit)
}

func TestFile(t *testing.T) {
	for _, c := range []struct{ spans, temp, want string }{
		{spans: "/x/s.jsonl", temp: "/tmp/r", want: "/x/s.jsonl"},
		{temp: "/tmp/r", want: "/tmp/r/ghatrace-spans.jsonl"},
		{want: ""},
	} {
		t.Setenv("GHATRACE_SPANS", c.spans)
		t.Setenv("RUNNER_TEMP", c.temp)
		assert.Equal(t, c.want, ghatrace.File())
	}
}

func TestRecordOffARunnerWritesNothing(t *testing.T) {
	t.Setenv("GHATRACE_SPANS", "")
	t.Setenv("RUNNER_TEMP", "")
	t.Chdir(t.TempDir())
	require.NoError(t, ghatrace.Record(ghatrace.Span{Name: "a"}))
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestReadNamesTheLineThatIsNotASpan(t *testing.T) {
	_, err := ghatrace.Read(strings.NewReader("{\"name\":\"a\"}\n\nnot json\n"))
	assert.ErrorContains(t, err, "line 3")
}
