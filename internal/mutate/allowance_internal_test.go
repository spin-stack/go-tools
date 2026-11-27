package mutate

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

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
	t.Parallel()
	dir, pid, m := settles(t, 3*time.Second, 0, true, "- becomes +")
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
		!strings.Contains(said.String(), " ran past 10s, and ") {
		t.Errorf("the edit that hangs was not refused at its allowance: %s", said.String())
	}

	unbroken := NewUnbroken()
	covers := &coverages{by: map[string]*coverage{}, timeout: "10m", slots: Slots(t.TempDir(), 1), unbroken: unbroken}
	covers.of(t.Context(), dir)
	answer, err := runAlone(t.Context(), m, t.TempDir(), Asked{Timeout: "10m", Unbroken: unbroken, Tree: NewTree(t.TempDir())}, nil, &said)
	if err != nil {
		t.Fatal(err)
	}
	if why := stoppedBy(answer); answer.Outcome != TimedOut || why != " (TestSettle ran past 10s, and was stopped)" {
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

// A test slower than the margin every test is allowed is given its own time several times over:
// an edit its slow test lets through survives, and is not refused for the time the test takes
// anyway.
func TestASlowTestIsAllowedItsOwnTime(t *testing.T) {
	t.Parallel()
	// An edit the test does not see, since it checks no count: no settling at all.
	_, _, m := settles(t, 0, 12*time.Second, false, "> becomes <=")
	var said strings.Builder
	tally, err := RunAll(t.Context(), []Mutation{m}, nil, "10m", 1, &said)
	if err != nil {
		t.Fatal(err)
	}
	if tally.Killed != 0 || strings.Contains(said.String(), "ran past") {
		t.Errorf("a test of twelve seconds was stopped short of its time: %s", said.String())
	}
}

// A run is allowed four times what its tests took with nothing broken, and ten seconds: the tests
// it asks, or every one where it asks them all, as an importer's run took them together where that
// was measured; each test four times its own. -timeout where one of them was not measured, and
// never more. A package is the same by its directory however it is named.
func TestARunIsAllowedAMultipleOfWhatItsTestsTook(t *testing.T) {
	unbroken := NewUnbroken()
	unbroken.recordTests("testdata", map[string]time.Duration{"TestA": time.Second, "TestB": 2 * time.Second})
	importer := t.TempDir()
	unbroken.recordTests(importer, map[string]time.Duration{"TestA": time.Second})
	unbroken.recordWhole(importer, 30*time.Second)
	measured, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		dir     string
		only    []string
		ceiling time.Duration
		want    time.Duration
	}{
		{"the tests it asks", measured, []string{"TestA"}, 5 * time.Minute, 14 * time.Second},
		{"every test", "testdata", nil, 5 * time.Minute, 22 * time.Second},
		{"a test not measured", measured, []string{"TestA", "TestC"}, 5 * time.Minute, 5 * time.Minute},
		{"an importer's run, as it took", importer, nil, 5 * time.Minute, 130 * time.Second},
		{"an importer's tests it asks", importer, []string{"TestA"}, 5 * time.Minute, 14 * time.Second},
		{"a package not measured", t.TempDir(), nil, 5 * time.Minute, 5 * time.Minute},
		{"past -timeout", "testdata", nil, 15 * time.Second, 15 * time.Second},
	} {
		if got := unbroken.allowance(c.dir, c.only, c.ceiling); got.Run != c.want {
			t.Errorf("%s: allowed %s, want %s", c.name, got.Run, c.want)
		}
	}
	if each := unbroken.allowance(measured, []string{"TestA"}, time.Minute).Each; each["TestA"] != 14*time.Second || each["TestB"] != 18*time.Second {
		t.Errorf("each test is allowed %v, not four times its own and ten seconds", each)
	}
}

// A test is stopped once it runs past its own allowance, from when -test.v says it starts, and
// only then: not once it has ended or paused to run beside the others, whatever runs after it.
// What a binary says is read as lines, however it comes.
func TestATestIsStoppedPastItsOwnAllowance(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		said []string
		want string
	}{
		{"past its allowance", []string{"=== RUN   TestSlow\n"}, "TestSlow"},
		{"ended in time, before one nobody measured", []string{"=== RUN   TestSlow\n--- PASS: TestSlow (0.00s)\n=== RUN   TestOther\n"}, ""},
		{"failed in time", []string{"=== RUN   TestSlow\n    --- FAIL: TestSlow/sub (0.00s)\n--- FAIL: TestSlow (0.00s)\n"}, ""},
		{"paused to run beside the others", []string{"=== RUN   TestSlow\n=== PAUSE TestSlow\n"}, ""},
		{"a subtest's start, which is its test's time", []string{"=== RUN   TestSlow\n=== RUN   TestSlow/sub\n"}, "TestSlow"},
		{"after an empty line, in pieces", []string{"\n=== RU", "N   TestSlow\r\n"}, "TestSlow"},
		{"after what a test printed with no newline", []string{"waiting=== RUN   TestSlow\n"}, "TestSlow"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			stopped := make(chan struct{})
			p := &pacer{each: map[string]time.Duration{"TestSlow": 50 * time.Millisecond}, stop: func() { close(stopped) }}
			for _, piece := range c.said {
				if _, err := p.Write([]byte(piece)); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-stopped:
			case <-time.After(500 * time.Millisecond):
			}
			if test, allowed := p.stopped(); test != c.want || (test != "" && allowed != 50*time.Millisecond) {
				t.Errorf("stopped %q at %s, want %q", test, allowed, c.want)
			}
		})
	}
}
