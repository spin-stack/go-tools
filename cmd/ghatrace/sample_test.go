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

// testdata/machine is a machine's /proc and /sys: two CPUs, pressure, a disk with a partition, and
// a loop device and a device-mapper one, which are no disk of their own.
func TestMachineReadsWhatTheMachineHas(t *testing.T) {
	at := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	assert.Equal(t, ghatrace.Sample{
		At: at,
		CPU: &ghatrace.CPU{
			CPUs: 2, User: 4848343, Nice: 4150851, System: 2776678, Idle: 68434844, IOWait: 4647453, SoftIRQ: 78975, Steal: 12,
		},
		Pressure: &ghatrace.Pressure{CPUSome: 111, IOSome: 4049326339, IOFull: 3723716333, MemorySome: 222, MemoryFull: 33},
		Disks:    []ghatrace.Disk{{Name: "nvme0n1", Read: 296800151 * 512, Written: 1093272602 * 512, BusyMS: 8087564}},
	}, machine(filepath.Join("testdata", "machine"), at))
}

// A kernel without PSI, and a machine with no disk of its own - a container's view - have neither,
// and the sampler says what it does without.
func TestMachineWithoutPressureOrDisks(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "proc"), 0o750))
	stat, err := os.ReadFile(filepath.Join("testdata", "machine", "proc", "stat"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "proc", "stat"), stat, 0o600))
	s := machine(root, time.Time{})
	assert.Nil(t, s.Pressure)
	assert.Empty(t, s.Disks)
	has, hasNot := what(s)
	assert.Equal(t, []string{"/proc/stat (2 CPUs)"}, has)
	assert.Equal(t, []string{"/proc/pressure", "/proc/diskstats' disks"}, hasNot)

	has, hasNot = what(machine(t.TempDir(), time.Time{}))
	assert.Empty(t, has, "a machine with no /proc, such as a Mac")
	assert.Len(t, hasNot, 3)
}

func TestSampleUntilWritesASampleEachTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spans.jsonl")
	t.Setenv("GHATRACE_SPANS", path)
	t.Setenv("RUNNER_NAME", "r7")
	require.NoError(t, sampleUntil(t.Context(), filepath.Join("testdata", "machine"), path, 20*time.Millisecond, 70*time.Millisecond))
	f, err := os.Open(path) //nolint:gosec // the test's own file
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	spans, samples, err := ghatrace.ReadAll(f)
	require.NoError(t, err)
	assert.Empty(t, spans)
	assert.GreaterOrEqual(t, len(samples), 3, "one at once, then one each 20ms for 70ms")
	assert.LessOrEqual(t, len(samples), 5)
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
