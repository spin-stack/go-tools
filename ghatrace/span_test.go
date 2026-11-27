package ghatrace_test

import (
	"encoding/json"
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

// Samples share the spans' file. Read, which callers had before samples, still gives spans alone,
// and a ghatrace from before samples reads a sample's line as a span of no runner and no time,
// which no job ran.
func TestSamplesShareTheFileWithSpans(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spans.jsonl")
	t.Setenv("GHATRACE_SPANS", path)
	t.Setenv("RUNNER_NAME", "r7")
	t.Setenv("GITHUB_JOB", "lane")
	at := time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC)
	usage := &ghatrace.Usage{User: time.Second, System: 2 * time.Millisecond, MaxRSS: 1 << 20, Read: 512, Written: 1024}
	require.NoError(t, ghatrace.Record(ghatrace.Span{Name: "a", Start: at, End: at, Usage: usage}))
	require.NoError(t, ghatrace.RecordSample(ghatrace.Sample{At: at, CPU: &ghatrace.CPU{CPUs: 2, Idle: 9}}))
	require.NoError(t, ghatrace.Record(ghatrace.Span{Name: "b", Start: at, End: at}))

	raw, err := os.ReadFile(path) //nolint:gosec // the test's own file
	require.NoError(t, err)
	spans, samples, err := ghatrace.ReadAll(strings.NewReader(string(raw)))
	require.NoError(t, err)
	require.Len(t, spans, 2)
	assert.Equal(t, usage, spans[0].Usage)
	assert.Nil(t, spans[1].Usage, "a span that ran no command says nothing of one")
	require.Len(t, samples, 1)
	assert.Equal(t, ghatrace.Sample{Runner: "r7", Job: "lane", At: at, CPU: &ghatrace.CPU{CPUs: 2, Idle: 9}}, samples[0])

	only, err := ghatrace.Read(strings.NewReader(string(raw)))
	require.NoError(t, err)
	assert.Equal(t, spans, only)

	// The span as ghatrace read it before samples.
	var before struct {
		Name   string    `json:"name"`
		Start  time.Time `json:"start"`
		Runner string    `json:"runner"`
	}
	sampleLine := strings.Split(string(raw), "\n")[1]
	assert.True(t, strings.HasPrefix(sampleLine, `{"sample":{"runner":"r7"`), "a sample's line holds the sample alone: %s", sampleLine)
	require.NoError(t, json.Unmarshal([]byte(sampleLine), &before))
	assert.Zero(t, before)
}
