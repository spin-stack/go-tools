package mutate

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
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

// bounded is combined, with cmd and every process it starts stopped once they hold more than
// memLimit between them; exceeded says they were. A go test is the go command, its compiler and
// the test binary, and the binary is the one that grows.
func bounded(what string, cmd *exec.Cmd) (out []byte, exceeded bool, err error) {
	limit := memLimit
	if limit == 0 {
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
	// Its own scan of /proc each tick, a couple of milliseconds: at -j 10 a fifth of one core,
	// beside the ten test processes it measures.
	done, stopped := make(chan struct{}), make(chan bool)
	go func() { stopped <- watch(cmd.Process, limit, done) }()
	err = cmd.Wait()
	close(done)
	exceeded = <-stopped
	account(what, cmd, time.Since(started))
	return buf.Bytes(), exceeded, err
}

// watch measures proc's tree every memPoll until done, and stops it once it holds more than
// limit, which it answers.
func watch(proc *os.Process, limit int64, done <-chan struct{}) bool {
	tick := time.NewTicker(memPoll)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return false
		case <-tick.C:
			if stopOver(proc, limit) {
				return true
			}
		}
	}
}

// stopOver kills proc and every process under it when together they hold more than limit, and
// says whether it did.
func stopOver(proc *os.Process, limit int64) bool {
	tree, rss := treeOf(processes(), proc.Pid)
	if rss <= limit {
		return false
	}
	// The root through the handle Start gave, which never signals a pid reused after it was
	// waited for. Its tree by pid, which a pidfd makes safe on Linux, where alone /proc names a
	// child: go test's binary does not die with the go command, and would go on growing.
	_ = proc.Kill()
	for _, child := range tree[1:] {
		p, _ := os.FindProcess(child)
		_ = p.Kill()
	}
	return true
}

// treeOf is root and every process under it in procs, root first, and what they hold between
// them.
func treeOf(procs map[int]process, root int) ([]int, int64) {
	children := map[int][]int{}
	for pid, p := range procs {
		children[p.ppid] = append(children[p.ppid], pid)
	}
	// Seen, since /proc is read a process at a time: a pid reused while it is read can close a
	// loop of parents, which would be walked for ever.
	tree, seen := []int{root}, map[int]bool{root: true}
	var rss int64
	for i := 0; i < len(tree); i++ {
		rss += procs[tree[i]].rss
		for _, child := range children[tree[i]] {
			if !seen[child] {
				seen[child] = true
				tree = append(tree, child)
			}
		}
	}
	return tree, rss
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
