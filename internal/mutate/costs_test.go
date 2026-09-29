package mutate

import (
	"os/exec"
	"testing"
	"time"
)

// Every process is counted under what it was for, one each time, with the CPU it and its children
// used; one that never started is counted too, as the time spent failing to start it.
func TestAProcessIsCountedUnderWhatItWasFor(t *testing.T) {
	before := map[string]Cost{}
	for _, c := range Costs() {
		before[c.What] = c
	}
	var used, system time.Duration
	for range 2 {
		// Copying from the kernel to the kernel is system time a tick can see; go version's was
		// none on a CI runner, where user minus system is user plus system.
		cmd := exec.Command("dd", "if=/dev/zero", "of=/dev/null", "bs=1M", "count=2000")
		if _, err := output("test: dd", cmd); err != nil {
			t.Fatal(err)
		}
		used += cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()
		system += cmd.ProcessState.SystemTime()
	}
	if system == 0 {
		t.Fatal("dd spent no system time, so nothing here tells it from user time")
	}
	if _, err := combined("test: nothing", exec.Command("there-is-no-such-command")); err == nil {
		t.Fatal("a command that is not there ran")
	}
	after := map[string]Cost{}
	for _, c := range Costs() {
		after[c.What] = c
	}
	ran := after["test: dd"]
	if got := ran.N - before["test: dd"].N; got != 2 {
		t.Errorf("dd counted %d times, want 2", got)
	}
	// Its user and its system time both: a go build's compilers spend both.
	if got := ran.CPU - before["test: dd"].CPU; got != used {
		t.Errorf("dd counted %s of CPU, its processes used %s", got, used)
	}
	if ran.Wall <= before["test: dd"].Wall {
		t.Errorf("dd took no time: %+v", ran)
	}
	if missing := after["test: nothing"]; missing.N-before["test: nothing"].N != 1 || missing.CPU != before["test: nothing"].CPU {
		t.Errorf("a command that never started: %+v", missing)
	}
	costs := Costs()
	for i := 1; i < len(costs); i++ {
		if costs[i].CPU > costs[i-1].CPU {
			t.Errorf("costs are not the most CPU first: %v", costs)
		}
	}
}
