package mutate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The memory free is MemAvailable's, in bytes; a meminfo without it, or with one unreadable, is
// none, which bounds nothing.
func TestTheMemoryFreeIsMemAvailable(t *testing.T) {
	meminfo := "MemTotal:       32554040 kB\nMemFree:         1234567 kB\nMemAvailable:   12345678 kB\nBuffers:          102400 kB\n"
	if got := memAvailable([]byte(meminfo)); got != 12345678<<10 {
		t.Errorf("MemAvailable of 12345678 kB read as %d bytes", got)
	}
	for _, meminfo := range []string{"", "MemTotal:       32554040 kB\n", "MemAvailable:   lots kB\n"} {
		if got := memAvailable([]byte(meminfo)); got != 0 {
			t.Errorf("%q read as %d bytes free", meminfo, got)
		}
	}
}

// A process's parent and memory are counted from the last parenthesis, whatever its name holds,
// and its memory in pages; a line cut short, or with a field that is no number, is no process.
func TestAProcessIsReadFromItsStat(t *testing.T) {
	stat := "4242 (a) (b c) S 17 4242 4242 0 -1 4194560 100 0 0 0 5 3 0 0 20 0 8 0 1000 2000000 300 18446744073709551615\n"
	if p, ok := parseStat([]byte(stat), 4096); !ok || p != (process{ppid: 17, rss: 300 * 4096}) {
		t.Errorf("read as %+v, %v; want parent 17 and 300 pages", p, ok)
	}
	shortest := "1 (x) S 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 7"
	if p, ok := parseStat([]byte(shortest), 1); !ok || p.rss != 7 {
		t.Errorf("a stat that ends at its memory read as %+v, %v", p, ok)
	}
	for _, stat := range []string{
		"",
		"1 (x) S 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0",
		"1 (x) S parent 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 7",
		"1 (x) S 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 pages",
	} {
		if p, ok := parseStat([]byte(stat), 1); ok {
			t.Errorf("%q read as %+v", stat, p)
		}
	}
}

// Every job is held to its share of the memory free when the run starts, or to what it is given;
// a negative is no bound.
func TestEachJobIsHeldToItsShareOfTheMemory(t *testing.T) {
	t.Cleanup(func() { testProcs, memLimit = 0, 0 })
	free := available()
	if free == 0 {
		t.Skip("no /proc/meminfo: there is no share to take")
	}
	Share(1, 0)
	// What is free moves while this runs; a tenth is far from half.
	if diff := memLimit - free; diff < -free/10 || diff > free/10 {
		t.Errorf("one job is held to %s, with %s free", gib(memLimit), gib(free))
	}
	Share(3, 5<<30)
	if memLimit != 5<<30 {
		t.Errorf("a job given 5 GiB is held to %s", gib(memLimit))
	}
	Share(3, -1)
	if memLimit != 0 {
		t.Errorf("a job given no bound is held to %s", gib(memLimit))
	}
}

// A tree is its root and everything under it, and holds what they all hold: go test's binary is
// the go command's child. A loop of parents, which a pid reused while /proc is read can make, is
// walked once.
func TestATreeIsEverythingUnderItsRoot(t *testing.T) {
	procs := map[int]process{
		10: {ppid: 1, rss: 100},
		11: {ppid: 10, rss: 20},
		12: {ppid: 11, rss: 3},
		20: {ppid: 1, rss: 1000},
	}
	tree, rss := treeOf(procs, 10)
	slices.Sort(tree)
	if !slices.Equal(tree, []int{10, 11, 12}) || rss != 123 {
		t.Errorf("the tree under 10 is %v, holding %d", tree, rss)
	}
	procs[10] = process{ppid: 12, rss: 100}
	tree, rss = treeOf(procs, 10)
	slices.Sort(tree)
	if !slices.Equal(tree, []int{10, 11, 12}) || rss != 123 {
		t.Errorf("a loop of parents walked as %v, holding %d", tree, rss)
	}
	if tree, rss := treeOf(procs, 99); len(tree) != 1 || rss != 0 {
		t.Errorf("a process gone since it was listed walked as %v, holding %d", tree, rss)
	}
}

// A bound is named as it is held: memory alone when it is measured from /proc, and the tasks too
// in a cgroup, which may bound them alone.
func TestABoundIsNamedAsItIsHeld(t *testing.T) {
	t.Cleanup(func() { testCgroup, memLimit = "", 0 })
	memLimit = 3 << 30
	if got := bound(); got != "3.0 GiB" {
		t.Errorf("measured from /proc, the bound is named %q", got)
	}
	testCgroup = "/sys/fs/cgroup/run.scope"
	if got, want := bound(), fmt.Sprintf("3.0 GiB or %d tasks", pidsMax); got != want {
		t.Errorf("in a cgroup, the bound is named %q, want %q", got, want)
	}
	memLimit = 0
	if got, want := bound(), fmt.Sprintf("%d tasks", pidsMax); got != want {
		t.Errorf("in a cgroup with no memory bound, the bound is named %q, want %q", got, want)
	}
}

// A cancelled run stops a test process with everything it started: a test's own go build, left
// running when only the binary is killed, writes into the directory the run is removing.
func TestACancelledTestStopsWhatItStarted(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc: a process's children cannot be read")
	}
	t.Cleanup(func() { testCgroup = "" })
	testCgroup = ""
	pidFile := filepath.Join(t.TempDir(), "child")
	ctx, cancel := context.WithCancel(t.Context())
	cmd := exec.CommandContext(ctx, "sh", "-c", `sleep 300 & echo $! > "$0"; wait`, pidFile)
	// A child left running holds the output open; without this Wait waits for it, and the child is
	// gone by its own end before it is looked for.
	cmd.WaitDelay = time.Second
	done := make(chan error, 1)
	go func() {
		_, _, err := bounded("a test", cmd)
		done <- err
	}()
	var child int
	for deadline := time.Now().Add(10 * time.Second); child == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the test process never started its child")
		}
		raw, _ := os.ReadFile(pidFile)
		child, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("a cancelled test process ended as if it had run")
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat("/proc/" + strconv.Itoa(child)); err != nil {
			return
		}
		if time.Now().After(deadline) {
			if p, err := os.FindProcess(child); err == nil {
				_ = p.Kill()
			}
			t.Fatal("the test process's child outlived it")
		}
	}
}
