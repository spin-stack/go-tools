package mutate

import (
	"testing"
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
