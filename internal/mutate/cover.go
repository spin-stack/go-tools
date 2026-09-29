package mutate

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Coverage is which of a package's own tests execute which lines of its files. A mutation is put
// to the tests that reach its line and no others: running the whole package for every edit was
// most of what this gate cost - a package of database tests is seconds a run, and a change is
// hundreds of edits - and a test that never executes the line cannot refuse it.
type Coverage struct {
	// reached is, by file base name, each test's executed spans in that file.
	reached map[string]map[string][]span
	// blocks is, by file base name, every span the profile counts, executed or not.
	blocks map[string][]span
	// failing are the tests that fail with nothing broken: their failing refuses no edit.
	failing map[string]bool
}

type span struct{ from, to int }

// Reaching is the tests that execute line of file, which is in the package this was made for.
// Empty is none: nothing in the package's own tests goes there. Nil is not known - a line no
// block of the profile holds, which a switch's case expressions are - and Run then asks every
// test.
func (c *Coverage) Reaching(file string, line int) []string {
	if !inAny(c.blocks[filepath.Base(file)], line) {
		return nil
	}
	out := []string{}
	for test, spans := range c.reached[filepath.Base(file)] {
		if inAny(spans, line) && !c.failing[test] {
			out = append(out, test)
		}
	}
	sort.Strings(out)
	return out
}

func inAny(spans []span, line int) bool {
	for _, s := range spans {
		if s.from <= line && line <= s.to {
			return true
		}
	}
	return false
}

// Cover measures the package in dir: its test binary built once with coverage, then each of its
// tests run alone into a profile of its own, each holding one of slots while it runs. The slots
// are the caller's whole budget of test processes, shared with the mutations it runs: a budget
// of its own per package multiplied with theirs, and a database package's tests start a
// PostgreSQL per process. Nil where the package has no tests, or they would not build - Run then
// asks every test, as it would without this.
func Cover(dir, timeout string, slots chan struct{}) (*Coverage, error) {
	work, err := os.MkdirTemp("", "mutate-cover-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(work) }()
	bin := filepath.Join(work, "pkg.test")
	build := exec.Command("go", "test", "-c", "-cover", "-covermode=set", "-o", bin, ".")
	build.Dir = dir
	slots <- struct{}{}
	out, err := combined("cover: build the package's tests", build)
	<-slots
	if err != nil {
		return nil, fmt.Errorf("mutate: building %s's tests with coverage: %w: %s", dir, err, out)
	}
	if _, err := os.Stat(bin); err != nil {
		return nil, nil //nolint:nilnil // no test binary: the package has no tests
	}
	name := exec.Command("go", "list", "-f", "{{.ImportPath}}", ".")
	name.Dir = dir
	importPath, err := output("listing", name)
	if err != nil {
		return nil, fmt.Errorf("mutate: naming %s: %w", dir, err)
	}
	list := exec.Command(bin, "-test.list", ".*") //nolint:gosec // the binary this built
	list.Dir = dir                                // mutate-exempt: listing runs no test, so where it runs changes nothing it says
	listed, err := output("listing", list)
	if err != nil {
		return nil, fmt.Errorf("mutate: listing %s's tests: %w", dir, err)
	}
	var tests []string
	for _, name := range strings.Fields(string(listed)) {
		if strings.HasPrefix(name, "Test") {
			tests = append(tests, name)
		}
	}

	c := &Coverage{reached: map[string]map[string][]span{}, blocks: map[string][]span{}, failing: map[string]bool{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i, test := range tests {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			profile := filepath.Join(work, strconv.Itoa(i)+".cov")
			run := exec.Command(bin, "-test.run", "^"+test+"$", "-test.count=1", //nolint:gosec // the binary this built
				"-test.timeout", timeout, "-test.coverprofile", profile)
			run.Dir = dir
			// A failure here is the package's own tests' to report, not this gate's; what this
			// gate does with it is ask the test about no edit, since it fails whatever the edit -
			// and say so, since an edit only it reaches then reads as one nothing holds.
			said, runErr := combined("cover: run one test", run)
			failed := runErr != nil
			if failed {
				// mutate-exempt: how much of the failure is shown, not whether it is.
				shown := tail(said, 20)
				fmt.Fprintf(os.Stderr, "  (%s: %s fails with nothing broken, and is asked about no edit: %v)\n%s\n",
					dir, test, runErr, shown)
			}
			spans, blocks, err := readProfile(profile, strings.TrimSpace(string(importPath))+"/")
			if err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if failed {
				c.failing[test] = true
			}
			// Every profile of one binary counts the same blocks.
			for file, s := range blocks {
				c.blocks[file] = s
			}
			for file, s := range spans {
				if c.reached[file] == nil {
					c.reached[file] = map[string][]span{}
				}
				c.reached[file][test] = s
			}
		}()
	}
	wg.Wait()
	return c, nil
}

// tail is the last n lines of out.
func tail(out []byte, n int) string {
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	return strings.Join(lines[max(len(lines)-n, 0):], "\n")
}

// readProfile is the spans of a coverage profile, by the base name of the file, for the files
// directly in the package whose import path is prefix: those executed, and all of them.
func readProfile(path, prefix string) (executed, all map[string][]span, err error) {
	f, err := os.Open(path) //nolint:gosec // a profile this wrote
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	executed, all = map[string][]span{}, map[string][]span{}
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		// name.go:12.3,14.5 2 1 - the file, the block's lines, its statements, whether it ran.
		// A line with no colon is no block: what follows it is empty and is refused below.
		file, rest, _ := strings.Cut(lines.Text(), ":")
		name, inPackage := strings.CutPrefix(file, prefix)
		if !inPackage || strings.Contains(name, "/") {
			continue
		}
		block, _, _ := strings.Cut(rest, " ")
		from, to, ok := strings.Cut(block, ",")
		if !ok {
			continue
		}
		a, errA := strconv.Atoi(strings.Split(from, ".")[0])
		b, errB := strconv.Atoi(strings.Split(to, ".")[0])
		if errA != nil || errB != nil {
			continue
		}
		all[name] = append(all[name], span{a, b})
		if !strings.HasSuffix(rest, " 0") {
			executed[name] = append(executed[name], span{a, b})
		}
	}
	return executed, all, lines.Err()
}
