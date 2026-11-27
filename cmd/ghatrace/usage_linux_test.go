package main

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/ghatrace"
)

// spanOf runs `ghatrace span` over argv with the test binary as the helper GHATRACE_TEST_HELPER
// names, and is the span it recorded.
func spanOf(t *testing.T, helper string, argv ...string) ghatrace.Span {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spans.jsonl")
	t.Setenv("GHATRACE_SPANS", path)
	t.Setenv("GHATRACE_TEST_HELPER", helper)
	require.NoError(t, run(t.Context(), append([]string{"span", "--"}, argv...)))
	f, err := os.Open(path) //nolint:gosec // the test's own file
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	spans, err := ghatrace.Read(f)
	require.NoError(t, err)
	require.Len(t, spans, 1)
	require.NotNil(t, spans[0].Usage)
	return spans[0]
}

// A span says how much of its time its command worked: a command that burns CPU used its time on
// one, one that sleeps next to none. The CPU is the whole tree's that the command waited for: a
// shell's two children, one after the other.
func TestASpanSaysWhetherItsCommandWorkedOrWaited(t *testing.T) {
	exe, err := os.Executable()
	require.NoError(t, err)

	burned := spanOf(t, "burn", "sh", "-c", `"$0"; "$0"`, exe)
	assert.GreaterOrEqual(t, burned.Usage.CPU(), 600*time.Millisecond, "two children, each 300ms of CPU")

	slept := spanOf(t, "sleep", exe)
	wall := slept.End.Sub(slept.Start)
	assert.GreaterOrEqual(t, wall, 400*time.Millisecond)
	assert.Less(t, slept.Usage.CPU(), wall/4, "asleep: %s of CPU in %s", slept.Usage.CPU(), wall)
}

// What a command reads from a disk and writes to one, and its largest process's memory. A file on
// tmpfs is in memory and never on a disk: what it moves there is 0, so the test needs a disk.
func TestASpanSaysWhatItsCommandMovedToADisk(t *testing.T) {
	dir := t.TempDir()
	var fs syscall.Statfs_t
	require.NoError(t, syscall.Statfs(dir, &fs))
	if fs.Type == 0x01021994 { // TMPFS_MAGIC
		t.Skipf("%s is on tmpfs, where a command reads and writes no disk", dir)
	}
	exe, err := os.Executable()
	require.NoError(t, err)
	t.Setenv("GHATRACE_TEST_DIR", dir)
	path := filepath.Join(t.TempDir(), "spans.jsonl")
	t.Setenv("GHATRACE_SPANS", path)
	t.Setenv("GHATRACE_TEST_HELPER", "disk")
	if err := run(t.Context(), []string{"span", "--", exe}); err == exitError(3) {
		t.Skipf("%s takes no O_DIRECT read", dir)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // the test's own file
	require.NoError(t, err)
	spans, err := ghatrace.Read(bytes.NewReader(raw))
	require.NoError(t, err)
	require.Len(t, spans, 1)
	u := spans[0].Usage
	require.NotNil(t, u)
	assert.GreaterOrEqual(t, u.Written, int64(diskBytes), "written, and synced")
	assert.GreaterOrEqual(t, u.Read, int64(diskBytes), "read past the page cache")
	assert.GreaterOrEqual(t, u.MaxRSS, int64(memoryBytes))
	assert.Equal(t, 0, spans[0].Exit)
}
