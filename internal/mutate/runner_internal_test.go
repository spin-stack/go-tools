package mutate

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// An edit in a package whose coverage could not be measured is asked of every test, not of none:
// no coverage is not knowing which tests reach the line, and a test that reaches nothing is only
// what a measurement says.
func TestAnEditWithNoCoverageIsAskedOfEveryTest(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":       "module example.com/nocover\n\ngo 1.24\n",
		"code.go":      "package nocover\n\nfunc Double(n int) int {\n\treturn n + n\n}\n",
		"code_test.go": "package nocover\n\nimport \"testing\"\n\nfunc TestDouble(t *testing.T) {\n\tif Double(2) != 4 {\n\t\tt.Fatal(\"not doubled\")\n\t}\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := Plan(filepath.Join(dir, "code.go"), map[int]bool{4: true})
	if err != nil || len(plan) == 0 {
		t.Fatalf("plan: %v, %v", plan, err)
	}
	for i := range plan {
		plan[i].ID = i + 1
	}
	s, err := Schemata(dir, plan, t.TempDir())
	if err != nil || s.Binary == "" || len(s.Built) == 0 {
		t.Fatalf("schema: %+v, %v", s, err)
	}
	p := pkgRun{dir: dir, timeout: "2m", slots: make(chan struct{}, 1)}
	for _, m := range s.Built {
		if got := p.built(m, s, nil); got != Killed {
			t.Errorf("%s: %v with no coverage, want refused by the test nobody measured", m, got)
		}
	}
}

// An edit that flips the only exit of a loop that appends has it allocate for ever: its tests are
// stopped at the bound and the edit is refused, named with it, in the package's binary and built
// alone - where the go command and its compiler are in the tree measured, and the test binary its
// child, which is stopped with it. The fixture gives up at a gigabyte and sleeps, so a bound that
// does not hold fails here rather than taking the machine.
func TestAnEditThatAllocatesForEverIsStoppedAndRefused(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod": "module example.com/grows\n\ngo 1.24\n",
		"code.go": "package grows\n\nimport (\n\t\"path\"\n\t\"time\"\n)\n\nfunc Ancestors(dir string) []string {\n\tvar out []string\n" +
			"\tfor d := path.Dir(path.Clean(dir)); len(out) < 1<<26; d = path.Dir(d) {\n\t\tout = append(out, d)\n" +
			"\t\tif d == \"/\" {\n\t\t\treturn out\n\t\t}\n\t}\n\ttime.Sleep(time.Hour)\n\treturn nil\n}\n",
		"code_test.go": "package grows\n\nimport \"testing\"\n\nfunc TestAncestors(t *testing.T) {\n" +
			"\tif got := Ancestors(\"/srv\"); len(got) != 1 {\n\t\tt.Fatalf(\"%d ancestors\", len(got))\n\t}\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if available() == 0 {
		t.Skip("no /proc/meminfo: nothing here is measured")
	}
	all, err := Plan(filepath.Join(dir, "code.go"), map[int]bool{12: true})
	if err != nil {
		t.Fatal(err)
	}
	// The loop's own edits are planned with its line too; the one that never leaves it is asked.
	plan := slices.DeleteFunc(all, func(m Mutation) bool { return m.What != "== becomes !=" })
	if len(plan) != 1 {
		t.Fatalf("no edit flips the loop's exit: %v", all)
	}
	plan[0].ID = 1
	// Unbounded, which builds what the package imports: on a cold cache that is the standard
	// library, compiled a package a core, which is no test's memory.
	if outcome, out := Run(plan[0], Asked{Timeout: "2m"}); outcome != Survived {
		t.Fatalf("the package as it is was %v: %s", outcome, out)
	}
	memLimit = 256 << 20
	t.Cleanup(func() { memLimit = 0 })

	var said strings.Builder
	tally, err := RunAll(plan, nil, "2m", 1, &said)
	if err != nil {
		t.Fatal(err)
	}
	if tally.Killed != 1 || !strings.Contains(said.String(), "refused   "+plan[0].String()+" (its tests held over 0.2 GiB, and were stopped)") {
		t.Errorf("the edit in the package's binary was not refused at its bound: %s", said.String())
	}

	overlay, err := Overlay(plan[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// The package as it is, built and run the same way under the same bound, is let be: it is the
	// edit's loop that is stopped, not the go command and its compiler.
	if outcome, out := Run(plan[0], Asked{Timeout: "2m"}); outcome != Survived {
		t.Fatalf("the package as it is was %v under the bound: %s", outcome, out)
	}
	if outcome, out := Run(plan[0], Asked{Overlay: overlay, Timeout: "2m"}); outcome != Exceeded {
		t.Errorf("the edit built alone was %v, not stopped at its bound: %s", outcome, out)
	}
	// Killed, it is gone once the kernel has reaped it; left running, it sleeps for an hour.
	deadline := time.Now().Add(5 * time.Second)
	for left := running("grows.test"); len(left) > 0; left = running("grows.test") {
		if time.Now().After(deadline) {
			for _, p := range left {
				_ = p.Kill()
			}
			t.Fatal("the go command was stopped and its test binary left to run")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// running is every process whose program is called name: not one whose arguments name it.
func running(name string) []*os.Process {
	var out []*os.Process
	for pid := range processes() {
		cmdline, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
		program, _, _ := strings.Cut(string(cmdline), "\x00")
		if filepath.Base(program) == name {
			if p, err := os.FindProcess(pid); err == nil {
				out = append(out, p)
			}
		}
	}
	return out
}
