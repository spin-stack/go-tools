package mutate

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// memLimit is how many bytes a test process and every process it started may hold between them;
// 0 is no bound. -test.timeout bounds a test's time and nothing its memory: an edit that flips the
// only exit of a loop that appends allocates gigabytes a second, and before its minute is out it
// has the machine, and a CI runner's job with it.
var memLimit int64

// memPoll is how often the processes are measured: at the gigabyte a second such a loop takes,
// what one passes its bound by before it is stopped.
const memPoll = 100 * time.Millisecond

// available is the memory the machine has free; 0 where there is no /proc to read it from, which
// bounds nothing.
func available() int64 {
	meminfo, _ := os.ReadFile("/proc/meminfo")
	return memAvailable(meminfo)
}

// memAvailable is the MemAvailable line of a /proc/meminfo, in bytes; 0 where it has none.
func memAvailable(meminfo []byte) int64 {
	for line := range strings.Lines(string(meminfo)) {
		// MemAvailable:   12345678 kB
		if rest, ok := strings.CutPrefix(line, "MemAvailable:"); ok {
			kb, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
			return kb << 10
		}
	}
	return 0
}

// gib is n bytes as people read it.
func gib(n int64) string {
	return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
}

// watched are the processes running under memLimit, by pid, and whether each has been stopped
// for passing it. One scan of /proc measures all of them: a scan is milliseconds, and one per
// process would be a fifth of a core at -j 10.
var watched = struct {
	mu      sync.Mutex
	by      map[int]*watch
	polling sync.Once
}{by: map[int]*watch{}}

type watch struct {
	proc *os.Process
	// limit is memLimit when it started, which the poll reads rather than memLimit: its goroutine
	// outlives what set it.
	limit    int64
	exceeded bool
}

// bounded is combined, with cmd and every process it starts stopped once they hold more than
// memLimit between them; exceeded says they were. A go test is the go command, its compiler and
// the test binary, and the binary is the one that grows.
func bounded(what string, cmd *exec.Cmd) (out []byte, exceeded bool, err error) {
	if memLimit == 0 {
		out, err := combined(what, cmd)
		return out, false, err
	}
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	started := time.Now()
	if err := cmd.Start(); err != nil {
		account(what, cmd, time.Since(started))
		return nil, false, err
	}
	w := &watch{proc: cmd.Process, limit: memLimit}
	watched.mu.Lock()
	watched.by[cmd.Process.Pid] = w
	watched.mu.Unlock()
	watched.polling.Do(func() { go poll() })
	err = cmd.Wait()
	watched.mu.Lock()
	delete(watched.by, cmd.Process.Pid)
	exceeded = w.exceeded
	watched.mu.Unlock()
	account(what, cmd, time.Since(started))
	return buf.Bytes(), exceeded, err
}

// poll measures the watched processes every memPoll, for as long as the run lasts.
func poll() {
	for range time.Tick(memPoll) {
		watched.mu.Lock()
		stopOver()
		watched.mu.Unlock()
	}
}

// stopOver kills every watched process whose tree holds more than its limit, and all of its
// tree. The caller holds watched.mu.
func stopOver() {
	procs := processes()
	children := map[int][]int{}
	for pid, p := range procs {
		children[p.ppid] = append(children[p.ppid], pid)
	}
	for pid, w := range watched.by {
		tree := []int{pid}
		var rss int64
		for i := 0; i < len(tree); i++ {
			rss += procs[tree[i]].rss
			tree = append(tree, children[tree[i]]...)
		}
		if rss <= w.limit {
			continue
		}
		w.exceeded = true
		// The root through the handle Start gave, which never signals a pid reused after it was
		// waited for. Its tree by pid, which a pidfd makes safe on Linux, where alone /proc names a
		// child: go test's binary does not die with the go command, and would go on growing.
		_ = w.proc.Kill()
		for _, child := range tree[1:] {
			p, _ := os.FindProcess(child)
			_ = p.Kill()
		}
	}
}

type process struct {
	ppid int
	rss  int64
}

// processes is every process /proc lists, with its parent and resident memory; none where there
// is no /proc, which bounds nothing.
func processes() map[int]process {
	out := map[int]process{}
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		// Unread when it is gone since it was listed, which parses as nothing.
		stat, _ := os.ReadFile("/proc/" + e.Name() + "/stat")
		if p, ok := parseStat(stat, int64(os.Getpagesize())); ok {
			out[pid] = p
		}
	}
	return out
}

// parseStat is a /proc/<pid>/stat's parent and resident memory, which it counts in pages of page
// bytes.
func parseStat(stat []byte, page int64) (process, bool) {
	// pid (comm) state ppid ... rss is the 24th field; comm may hold spaces and parentheses, so
	// the fields are counted from its last.
	fields := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
	if len(fields) < 22 {
		return process{}, false
	}
	ppid, errP := strconv.Atoi(fields[1])
	rss, errR := strconv.ParseInt(fields[21], 10, 64)
	if errP != nil || errR != nil {
		return process{}, false
	}
	return process{ppid: ppid, rss: rss * page}, true
}
