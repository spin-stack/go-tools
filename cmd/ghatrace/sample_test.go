package main

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

// testdata/machine is a machine's /proc and /sys: two CPUs, pressure, a disk with a partition and
// one whose line is a 2.6 kernel's, and a loop device and a device-mapper one, which are no disk of
// their own.
func TestMachineReadsWhatTheMachineHas(t *testing.T) {
	at := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	s := machine(filepath.Join("testdata", "machine"), at)
	assert.Equal(t, ghatrace.Sample{
		At: at,
		CPU: &ghatrace.CPU{
			CPUs: 2, User: 4848343, Nice: 4150851, System: 2776678, Idle: 68434844, IOWait: 4647453, SoftIRQ: 78975, Steal: 12,
		},
		Pressure: &ghatrace.Pressure{CPUSome: 111, IOSome: 4049326339, IOFull: 3723716333, MemorySome: 222, MemoryFull: 33},
		Disks: []ghatrace.Disk{
			{Name: "nvme0n1", Read: 296800151 * 512, Written: 1093272602 * 512, BusyMS: 8087564},
			{Name: "sda", Read: 4 * 512, Written: 6 * 512, BusyMS: 7},
		},
	}, s)
	said, ok := announce(s, time.Second, "/tmp/spans.jsonl")
	assert.True(t, ok)
	assert.Equal(t, "ghatrace: sampling /proc/stat (2 CPUs), /proc/pressure, /proc/diskstats (nvme0n1, sda) every 1s into /tmp/spans.jsonl", said)
}

// A kernel without PSI, and a machine with no disk of its own - a container's view - have neither,
// and the sampler says what it does without. A machine with no /proc, such as a Mac, has nothing.
func TestMachineWithoutPressureOrDisks(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "proc"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, "proc", "stat"), []byte("cpu  1 2 3 4 5 6 7 8\ncpu0 1 2 3 4 5 6 7 8\n"), 0o600))
	s := machine(root, time.Time{})
	assert.Equal(t, &ghatrace.CPU{CPUs: 1, User: 1, Nice: 2, System: 3, Idle: 4, IOWait: 5, IRQ: 6, SoftIRQ: 7, Steal: 8}, s.CPU, "a 2.6 kernel's line")
	assert.Nil(t, s.Pressure)
	assert.Empty(t, s.Disks)
	said, ok := announce(s, time.Second, "f")
	assert.True(t, ok)
	assert.Equal(t, "ghatrace: sampling /proc/stat (1 CPUs) every 1s into f; no /proc/pressure, /proc/diskstats () here", said)

	said, ok = announce(machine(t.TempDir(), time.Time{}), time.Second, "f")
	assert.False(t, ok)
	assert.Equal(t, "ghatrace: sample: nothing here to sample: no /proc", said)
}

func TestSampleUntilWritesASampleEachTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spans.jsonl")
	t.Setenv("GHATRACE_SPANS", path)
	t.Setenv("RUNNER_NAME", "r7")
	require.NoError(t, sampleUntil(t.Context(), filepath.Join("testdata", "machine"), path, 10*time.Millisecond, 200*time.Millisecond))
	f, err := os.Open(path) //nolint:gosec // the test's own file
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	spans, samples, err := ghatrace.ReadAll(f)
	require.NoError(t, err)
	assert.Empty(t, spans)
	assert.GreaterOrEqual(t, len(samples), 3, "one at once, then one each 10ms for 200ms")
	assert.LessOrEqual(t, len(samples), 22)
	for i, s := range samples {
		assert.Equal(t, "r7", s.Runner)
		assert.Equal(t, uint64(111), s.Pressure.CPUSome)
		if i > 0 {
			assert.True(t, s.At.After(samples[i-1].At))
		}
	}
}

func TestSampleOffARunnerSamplesNothing(t *testing.T) {
	t.Setenv("GHATRACE_SPANS", "")
	t.Setenv("RUNNER_TEMP", "")
	t.Chdir(t.TempDir())
	said := stderr(t, func() { require.NoError(t, run(t.Context(), []string{"sample"})) })
	assert.Equal(t, "ghatrace: sample: not on a runner and no $GHATRACE_SPANS: nothing sampled\n", said)
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	assert.Empty(t, entries)
	assert.True(t, strings.HasPrefix(usage, "usage: ghatrace run|span|gotest|sample"))
}

func TestSampleTakesOnlyLengthsOfTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spans.jsonl")
	t.Setenv("GHATRACE_SPANS", path)
	for _, args := range [][]string{{"-every", "0"}, {"-for", "0"}, {"-every", "-1s"}} {
		assert.ErrorContains(t, run(t.Context(), append([]string{"sample", "-foreground"}, args...)), "more than none", args)
	}
	require.NoError(t, run(t.Context(), []string{"sample", "-foreground", "-every", "1ns", "-for", "1ns"}))
	raw, err := os.ReadFile(path) //nolint:gosec // the test's own file
	require.NoError(t, err)
	assert.Contains(t, string(raw), `{"sample":`, "the shortest time there is, sampled")
}
