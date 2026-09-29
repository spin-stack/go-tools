package mutate

import (
	"os/exec"
	"path/filepath"
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

// A test's runs are added to its package's: the package asked, or, for a go test over several,
// the one go test names after them. Subtests are their test's, and a line that only looks like
// an end is not one.
func TestEachAskedTestIsCountedToItsPackage(t *testing.T) {
	find := func(pkg, test string) TestCost {
		for _, c := range TestCosts() {
			if c.Package == pkg && c.Test == test {
				return c
			}
		}
		return TestCost{}
	}
	countTests("p/one", []byte("=== RUN   TestSlow\n    --- PASS: TestSlow/sub (0.50s)\n--- PASS: TestSlow (1.50s)\n--- FAIL: TestFast (0.25s)\nFAIL\n"))
	countTests("p/one", []byte("--- PASS: TestSlow (2.00s)\r\nsaid: --- PASS: TestSlow (9.00s)\nPASS\n"))
	countTests("", []byte("--- PASS: TestA (1.00s)\nok  \texample.com/a\t1.2s\n--- FAIL: TestB (0.10s)\nFAIL\texample.com/b\t0.3s\n"))

	if got := find("p/one", "TestSlow"); got.N != 2 || got.Took != 3500*time.Millisecond {
		t.Errorf("TestSlow: %+v, want asked twice for 3.5s", got)
	}
	if got := find("p/one", "TestFast"); got.N != 1 || got.Took != 250*time.Millisecond {
		t.Errorf("TestFast: %+v", got)
	}
	if got := find("p/one", "TestSlow/sub"); got.N != 0 {
		t.Errorf("a subtest was counted as a test: %+v", got)
	}
	if got := find("example.com/a", "TestA"); got.N != 1 || got.Took != time.Second {
		t.Errorf("TestA: %+v, want example.com/a's", got)
	}
	if got := find("example.com/b", "TestB"); got.N != 1 || got.Took != 100*time.Millisecond {
		t.Errorf("TestB: %+v, want example.com/b's", got)
	}
}

// The report is the longest first, and of two tests that took as long, in their names' order;
// no entry is of no test.
func TestTheTestsAreTheLongestFirst(t *testing.T) {
	countTests("p/fast", []byte("--- PASS: TestQuick (0.01s)\n"))
	countTests("p/tie", []byte("--- PASS: TestZ (4.00s)\n--- PASS: TestY (4.00s)\n"))
	costs := TestCosts()
	var y, z int
	for i, c := range costs {
		if c.Test == "" {
			t.Errorf("an entry of no test: %v", costs)
		}
		if i > 0 && c.Took > costs[i-1].Took {
			t.Errorf("tests are not the longest first: %v", costs)
		}
		switch {
		case c.Package == "p/tie" && c.Test == "TestY":
			y = i
		case c.Package == "p/tie" && c.Test == "TestZ":
			z = i
		}
	}
	if y > z {
		t.Errorf("of two tests that took as long, TestZ came before TestY: %v", costs)
	}
}

// A package is named by its directory from this module's root, however it was asked: by its
// directory, relative or absolute, or by its import path.
func TestAPackageIsNamedByItsDirectory(t *testing.T) {
	abs, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, asked := range []string{".", abs, modulePath() + "/internal/mutate"} {
		if got := packageOf(asked); got != "internal/mutate" {
			t.Errorf("%s is named %q, want internal/mutate", asked, got)
		}
	}
	if got := packageOf("example.com/elsewhere"); got != "example.com/elsewhere" {
		t.Errorf("another module's package is named %q", got)
	}
}
