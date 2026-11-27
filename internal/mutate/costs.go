package mutate

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/modfile"
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

// workIn has cmd, a go command, keep its work directory under dir, which the caller removes: the
// go command removes its own as it exits, and killed when a run is stopped, it leaves one
// (go-build*) in TMPDIR, with a package's compiled archives in it, for every build it was making.
func workIn(cmd *exec.Cmd, dir string) {
	cmd.Env = append(os.Environ(), "GOTMPDIR="+dir)
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

// TestCost is what one test cost a run's asking: how many times an edit was put to it, and how
// long it ran in all. A test that starts a control plane and is asked of every edit in the code it
// reaches is where a gate's time goes, and the test, not the gate, is what to make faster.
type TestCost struct {
	Package string
	Test    string
	N       int
	Took    time.Duration
}

var testCosts = struct {
	mu sync.Mutex
	by map[[2]string]*TestCost
}{by: map[[2]string]*TestCost{}}

// TestCosts is every test asked so far, the longest in all first.
func TestCosts() []TestCost {
	testCosts.mu.Lock()
	defer testCosts.mu.Unlock()
	out := make([]TestCost, 0, len(testCosts.by))
	for _, c := range testCosts.by {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Took != out[j].Took {
			return out[i].Took > out[j].Took
		}
		return out[i].Package+" "+out[i].Test < out[j].Package+" "+out[j].Test
	})
	return out
}

var (
	// A top-level test's end, as -test.v says it; a subtest's is indented.
	testEnd = regexp.MustCompile(`^--- (?:PASS|FAIL|SKIP): (Test\S*) \(([0-9.]+)s\)$`)
	// go test's line for a package it ran, after its tests' lines.
	packageEnd = regexp.MustCompile(`^(?:ok|FAIL)\s+(\S+)\s`)
)

// modulePath is the import path of the module this runs in, "" outside one.
var modulePath = sync.OnceValue(func() string {
	root, err := moduleRoot(".")
	if err != nil {
		return ""
	}
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	return modfile.ModulePath(gomod)
})

// packageOf is how the report names a package: its directory from its module's root, whether it
// was asked by a directory, relative or not, or named by go test by its import path.
func packageOf(dirOrPath string) string {
	if rel, ok := strings.CutPrefix(dirOrPath, modulePath()+"/"); ok && modulePath() != "" {
		return rel
	}
	if info, err := os.Stat(dirOrPath); err != nil || !info.IsDir() {
		return dirOrPath // an import path outside this module
	}
	abs, err := filepath.Abs(dirOrPath)
	if err != nil {
		return dirOrPath
	}
	root, err := moduleRoot(abs)
	if err != nil {
		return dirOrPath
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return dirOrPath
	}
	return filepath.ToSlash(rel)
}

// testTimes is how long each top-level test out, a -test.v output, says it ran.
func testTimes(out string) map[string]time.Duration {
	took := map[string]time.Duration{}
	for line := range strings.Lines(out) {
		if test, d, ok := testEndOf(strings.TrimRight(line, "\r\n")); ok {
			took[test] = d
		}
	}
	return took
}

// testEndOf is the test whose end line is, and how long it says the test ran.
func testEndOf(line string) (test string, took time.Duration, ok bool) {
	m := testEnd.FindStringSubmatch(line)
	if m == nil {
		return test, took, false
	}
	// mutate-exempt: a bit size other than 32 is 64.
	seconds, _ := strconv.ParseFloat(m[2], 64) // the pattern's digits and point
	return m[1], time.Duration(seconds * float64(time.Second)), true
}

// countTests adds the tests out says ran to their package's: pkg's, or where pkg is "" - a go test
// over several packages - the one go test's line after them names.
func countTests(pkg string, out []byte) {
	var ran []TestCost
	add := func(pkg string) {
		pkg = packageOf(pkg)
		testCosts.mu.Lock()
		defer testCosts.mu.Unlock()
		for _, r := range ran {
			key := [2]string{pkg, r.Test}
			c := testCosts.by[key]
			if c == nil {
				c = &TestCost{Package: pkg, Test: r.Test}
				testCosts.by[key] = c
			}
			c.N++
			c.Took += r.Took
		}
		ran = nil
	}
	for line := range strings.Lines(string(out)) {
		line = strings.TrimRight(line, "\r\n")
		if test, took, ok := testEndOf(line); ok {
			ran = append(ran, TestCost{Test: test, Took: took})
			continue
		}
		if m := packageEnd.FindStringSubmatch(line); pkg == "" && m != nil {
			add(m[1])
		}
	}
	if pkg != "" {
		add(pkg)
	}
}
