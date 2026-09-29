package mutate

import (
	"os/exec"
	"sort"
	"sync"
	"time"
)

// Cost is what the processes a run started for one purpose took: how many, how long they ran end
// to end, and the CPU they and every process under them used - a go build's compilers, a test
// binary's own children. The run's account of where its time goes, so that making it cheaper is
// a measurement and not a guess.
type Cost struct {
	What string
	N    int
	Wall time.Duration
	CPU  time.Duration
}

var costs = struct {
	mu sync.Mutex
	by map[string]*Cost
}{by: map[string]*Cost{}}

// Costs is every purpose's Cost so far, the most CPU first.
func Costs() []Cost {
	costs.mu.Lock()
	defer costs.mu.Unlock()
	out := make([]Cost, 0, len(costs.by))
	for _, c := range costs.by {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CPU > out[j].CPU })
	return out
}

// combined is cmd's CombinedOutput, counted under what.
func combined(what string, cmd *exec.Cmd) ([]byte, error) {
	started := time.Now()
	out, err := cmd.CombinedOutput()
	account(what, cmd, time.Since(started))
	return out, err
}

// output is cmd's Output, counted under what.
func output(what string, cmd *exec.Cmd) ([]byte, error) {
	started := time.Now()
	out, err := cmd.Output()
	account(what, cmd, time.Since(started))
	return out, err
}

// account adds one process to what's Cost. A process that never started used no CPU, and its
// wall is the time spent failing to start it.
func account(what string, cmd *exec.Cmd, wall time.Duration) {
	var cpu time.Duration
	if ps := cmd.ProcessState; ps != nil {
		// The rusage wait answers counts the children the process waited for, so a go test's
		// compilers and link are in its CPU.
		cpu = ps.UserTime() + ps.SystemTime()
	}
	costs.mu.Lock()
	defer costs.mu.Unlock()
	c := costs.by[what]
	if c == nil {
		c = &Cost{What: what}
		costs.by[what] = c
	}
	c.N++
	c.Wall += wall
	c.CPU += cpu
}
