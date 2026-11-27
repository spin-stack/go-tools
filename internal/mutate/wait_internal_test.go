package mutate

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// These run the gate end to end, and wait for what they show: in a file of its own, after the
// others, so that an edit one of those refuses in a second is not first asked of these.

// shortMargin has every test and run allowed a second beyond its multiple, not ten, for t. Not
// beside other tests: they read it.
func shortMargin(t *testing.T) {
	t.Helper()
	saved := margin
	margin = time.Second
	t.Cleanup(func() { margin = saved })
}

// settles is a module whose Settle sleeps a millisecond n times, counting down to zero: an edit
// that counts up never reaches zero, and holds its caller asleep for ever, as an inverted wait loop
// does. TestPatience sleeps for patience first; TestSettle writes its process's ID to pid, sleeps
// for pause and asks Settle for three, checking the count where checked. The edit is what names.
func settles(t *testing.T, patience, pause time.Duration, checked bool, what string) (dir, pid string, m Mutation) {
	t.Helper()
	dir, pid = t.TempDir(), filepath.Join(t.TempDir(), "pid")
	for name, body := range map[string]string{
		"go.mod": "module example.com/settles\n\ngo 1.24\n",
		"code.go": "package settles\n\nimport \"time\"\n\nfunc Settle(n int) int {\n\tslept := 0\n" +
			"\tfor left := n; left > 0; left = left - 1 {\n\t\ttime.Sleep(time.Millisecond)\n\t\tslept++\n\t}\n\treturn slept\n}\n",
		"code_test.go": "package settles\n\nimport (\n\t\"os\"\n\t\"strconv\"\n\t\"testing\"\n\t\"time\"\n)\n\n" +
			fmt.Sprintf("func TestPatience(t *testing.T) {\n\ttime.Sleep(%d)\n}\n\n", patience) +
			"func TestSettle(t *testing.T) {\n" +
			fmt.Sprintf("\t_ = os.WriteFile(%q, []byte(strconv.Itoa(os.Getpid())), 0o600)\n", pid) +
			fmt.Sprintf("\ttime.Sleep(%d)\n\tif Settle(3) != 3 && %t {\n\t\tt.Fatal(\"not three\")\n\t}\n}\n", pause, checked),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	all, err := Plan(filepath.Join(dir, "code.go"), map[int]bool{7: true})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(all, func(m Mutation) bool { return m.What == what })
	if i < 0 {
		t.Fatalf("no edit %q: %v", what, all)
	}
	m = all[i]
	m.ID = 1
	return dir, pid, m
}

// An edit that holds its test asleep is stopped at a multiple of the time the test takes with
// nothing broken, and refused, named with what it ran past - not left to the -timeout, which is
// what a package's slowest whole run may take and holds one of the run's slots that long - with
// nothing it started left running. In the package's binary, where coverage names the test, and
// built alone with no line known, which asks every test: there the run as a whole is allowed the
// others' time too, and the test that hangs is stopped at its own.
func TestAnEditThatHangsItsTestIsStoppedAtItsAllowance(t *testing.T) {
	shortMargin(t)
	dir, pid, m := settles(t, time.Second, 0, true, "- becomes +")
	started := time.Now()
	var said strings.Builder
	tally, err := RunAll(t.Context(), []Mutation{m}, nil, "10m", 1, &said)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took > time.Minute {
		t.Errorf("the edit was stopped after %s, not at its allowance", took)
	}
	if tally.Killed != 1 || !strings.Contains(said.String(), "refused   "+m.String()+" (") ||
		!strings.Contains(said.String(), " ran past 1s, and ") {
		t.Errorf("the edit that hangs was not refused at its allowance: %s", said.String())
	}
	if len(tally.Refused) != 0 {
		t.Errorf("an edit stopped for its time is among the refusals a cache keeps, and a stall would never be asked again: %v", tally.Refused)
	}

	unbroken := NewUnbroken()
	covers := &coverages{by: map[string]*coverage{}, timeout: "10m", slots: Slots(t.TempDir(), 1), unbroken: unbroken}
	covers.of(t.Context(), dir)
	answer, err := runAlone(t.Context(), m, t.TempDir(), Asked{Timeout: "10m", Unbroken: unbroken, Tree: NewTree(t.TempDir())}, nil, &said)
	if err != nil {
		t.Fatal(err)
	}
	if why := stoppedBy(answer); answer.Outcome != TimedOut || why != " (TestSettle ran past 1s, and was stopped)" {
		t.Errorf("asked of every test, the edit was %v%s: %s", answer.Outcome, why, answer.Said)
	}
	written, err := os.ReadFile(pid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join("/proc", string(written))); err == nil {
		t.Errorf("the test binary, %s, was left running past its allowance", written)
	}
}

// With -timeout 0 and a test asked that nobody measured, the run has no bound; a test it asks
// that was measured is still stopped at its own allowance, and the edit refused for it.
func TestAnUnboundedRunStopsATestAtItsOwnAllowance(t *testing.T) {
	shortMargin(t)
	dir, _, m := settles(t, 0, 0, true, "- becomes +")
	unbroken := NewUnbroken()
	covers := &coverages{by: map[string]*coverage{}, timeout: "10m", slots: Slots(t.TempDir(), 1), unbroken: unbroken}
	covers.of(t.Context(), dir)
	// What stops the run where the edit's test is not: past it, the run fails, and the test says so.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	answer, err := runAlone(ctx, m, t.TempDir(), Asked{Timeout: "0", Unbroken: unbroken, Tree: NewTree(t.TempDir()),
		Reaching: []string{"TestSettle", "TestNobodyMeasured"}}, nil, io.Discard)
	if err != nil {
		t.Fatalf("the test that hangs was not stopped at its allowance: %v", err)
	}
	if why := stoppedBy(answer); answer.Outcome != TimedOut || why != " (TestSettle ran past 1s, and was stopped)" {
		t.Errorf("the edit was %v%s: %s", answer.Outcome, why, answer.Said)
	}
}

// A test slower than the margin every test is allowed is given its own time several times over:
// an edit its slow test lets through survives, and is not refused for the time the test takes
// anyway.
func TestASlowTestIsAllowedItsOwnTime(t *testing.T) {
	shortMargin(t)
	// An edit the test does not see, since it checks no count: no settling at all.
	_, _, m := settles(t, 0, 2*time.Second, false, "> becomes <=")
	var said strings.Builder
	tally, err := RunAll(t.Context(), []Mutation{m}, nil, "10m", 1, &said)
	if err != nil {
		t.Fatal(err)
	}
	if tally.Killed != 0 || strings.Contains(said.String(), "ran past") {
		t.Errorf("a test of two seconds was stopped short of its time: %s", said.String())
	}
}
