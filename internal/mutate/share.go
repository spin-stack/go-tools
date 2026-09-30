package mutate

import (
	"os"
	"runtime"
	"strconv"
)

// testProcs is the GOMAXPROCS of every test process a run starts; 0 leaves each the machine's.
// A test binary takes every core by default, and so does each of its t.Parallel tests: four
// mutations of the control plane's app package asked at once were nineteen processes running and
// a load of 24 on a 20-core machine, though they used a fraction of its CPU.
var testProcs int

// Share divides the machine among jobs test processes run at once, each held to its share of the
// cores and to memory bytes, with every process it starts; 0 is its share of what the machine has
// free now, which jobs at their bound at once cannot pass, and a negative is no bound. Call it
// once, before anything is asked.
func Share(jobs int, memory int64) {
	testProcs = max(runtime.NumCPU()/max(jobs, 1), 1)
	if memory == 0 {
		memory = available() / int64(max(jobs, 1))
	}
	memLimit = max(memory, 0)
}

// testEnv is the environment a test process runs in: this one's, its share of the machine, and
// extra.
func testEnv(extra ...string) []string {
	env := os.Environ()
	if testProcs > 0 {
		env = append(env, "GOMAXPROCS="+strconv.Itoa(testProcs))
	}
	return append(env, extra...)
}
