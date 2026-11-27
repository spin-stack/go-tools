package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/ghatrace"
)

// sample returns at once, as a step must to let the next start, and its sampler goes on in a
// process of its own until -for has passed. A second sample in the job starts no second sampler.
func TestSampleRunsInTheBackgroundOncePerJob(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spans.jsonl")
	t.Setenv("GHATRACE_SPANS", path)
	t.Setenv("GHATRACE_TEST_HELPER", "main")
	start := time.Now()
	said := stderr(t, func() {
		require.NoError(t, run(t.Context(), []string{"sample", "-every", "20ms", "-for", "3s"}))
	})
	assert.Less(t, time.Since(start), 2*time.Second, "returns before its sampler is done")
	assert.Regexp(t, `^ghatrace: sampling /proc/stat \(\d+ CPUs\)`, said)
	assert.Contains(t, said, "every 20ms into "+path)

	samples := func() []ghatrace.Sample {
		raw, err := os.ReadFile(path) //nolint:gosec // the test's own file
		if err != nil {
			return nil
		}
		_, s, err := ghatrace.ReadAll(bytes.NewReader(raw))
		require.NoError(t, err)
		return s
	}
	require.Eventually(t, func() bool { return len(samples()) >= 3 }, 5*time.Second, 10*time.Millisecond, "sampled after sample returned")

	said = stderr(t, func() { require.NoError(t, run(t.Context(), []string{"sample", "-every", "20ms"})) })
	assert.Equal(t, "ghatrace: sample: not sampling: this job's machine is sampled already\n", said)

	// The sampler lets go of the job's lock when -for has passed, and writes no more.
	require.Eventually(t, func() bool {
		unlock, err := lock(path + ".sampling")
		if err != nil {
			return false
		}
		unlock()
		return true
	}, 10*time.Second, 20*time.Millisecond)
	n := len(samples())
	assert.Less(t, n, 3000/20+5, "one sampler, not two")
	time.Sleep(100 * time.Millisecond)
	assert.Len(t, samples(), n)
}
