package mutate

import (
	"bufio"
	"cmp"
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Coverage is which of a package's own tests execute which code of its files. A mutation is put
// to the tests that run it and no others: running the whole package for every edit was most of
// what this gate cost - a package of database tests is seconds a run, and a change is hundreds of
// edits - and a test that never executes the edit cannot refuse it.
type Coverage struct {
	// reached is, by file base name, each test's executed spans in that file.
	reached map[string]map[string][]span
	// blocks is, by file base name, every span the profile counts, executed or not.
	blocks map[string][]span
	// failing are the tests that fail with nothing broken: their failing refuses no edit.
	failing map[string]bool
	// took is how long each test's process ran, alone and with nothing broken.
	took map[string]time.Duration
}

// place is a line and a column of a file, both from 1 and the column in bytes, as a coverage
// profile names where a block starts and ends.
type place struct{ line, col int }

func (p place) before(q place) bool { return p.line < q.line || p.line == q.line && p.col < q.col }

// span is one block of a profile: from where it starts up to where it ends, which is not in it.
type span struct{ from, to place }

// Reaching is the tests that run m, an edit planned in the package this was made for: those
// that execute a block holding the first of m's places one holds (Mutation.reach). Coverage
// counts no case of a switch or a select in a block, nor what follows a func literal in its
// statement; for those, that is the statement around the edit, since a test that evaluates a case
// has reached its switch. Not the case's own block, which counts the tests that chose the case
// and leaves out one that an edit makes choose it. Empty is none: nothing in the package's own
// tests goes there. Nil is not known - no block holds any of the places, or m was not planned -
// and Run then asks every test.
func (c *Coverage) Reaching(m Mutation) []string {
	file := filepath.Base(m.File)
	at := slices.IndexFunc(m.reach, func(p place) bool { return inAny(c.blocks[file], p) })
	if at < 0 {
		return nil
	}
	out := []string{}
	for test, spans := range c.reached[file] {
		if inAny(spans, m.reach[at]) && !c.failing[test] {
			out = append(out, test)
		}
	}
	sort.Strings(out)
	return out
}

func inAny(spans []span, p place) bool {
	for _, s := range spans {
		if !p.before(s.from) && p.before(s.to) {
			return true
		}
	}
	return false
}

// Cover measures the package in dir: its test binary built once with coverage, then each of its
// tests run alone into a profile of its own, each in one of slots' trees while it runs. The slots
// are the caller's whole budget of test processes, shared with the mutations it runs: a budget
// of its own per package multiplied with theirs, and a database package's tests start a
// PostgreSQL per process. Nil where the package has no tests, or they would not build - Run then
// asks every test, as it would without this.
func Cover(ctx context.Context, dir, timeout string, slots chan *Tree) (*Coverage, error) {
	work, err := os.MkdirTemp("", "mutate-cover-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(work) }()
	bin := filepath.Join(work, "pkg.test")
	build := exec.CommandContext(ctx, "go", "test", "-c", "-cover", "-covermode=set", "-o", bin, ".")
	build.Dir = dir
	workIn(build, work)
	tree := <-slots
	out, err := combined("cover: build the package's tests", build)
	slots <- tree
	if err != nil {
		return nil, fmt.Errorf("mutate: building %s's tests with coverage: %w: %s", dir, err, out)
	}
	if _, err := os.Stat(bin); err != nil {
		return nil, nil //nolint:nilnil // no test binary: the package has no tests
	}
	name := exec.CommandContext(ctx, "go", "list", "-f", "{{.ImportPath}}", ".")
	name.Dir = dir
	importPath, err := output("listing", name)
	if err != nil {
		return nil, fmt.Errorf("mutate: naming %s: %w", dir, err)
	}
	list := exec.CommandContext(ctx, bin, "-test.list", ".*") //nolint:gosec // the binary this built
	list.Dir = dir                                            // mutate-exempt: listing runs no test, so where it runs changes nothing it says
	listed, err := output("listing", list)
	if err != nil {
		return nil, fmt.Errorf("mutate: listing %s's tests: %w", dir, err)
	}
	var tests []string
	for name := range strings.FieldsSeq(string(listed)) {
		if runByDefault(name) {
			tests = append(tests, name)
		}
	}

	c := &Coverage{reached: map[string]map[string][]span{}, blocks: map[string][]span{}, failing: map[string]bool{},
		took: map[string]time.Duration{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var treeErr error
	for i, test := range tests {
		wg.Add(1)
		tree := <-slots
		go func() {
			defer wg.Done()
			defer func() { slots <- tree }()
			profile := filepath.Join(work, strconv.Itoa(i)+".cov")
			run := exec.CommandContext(ctx, bin, "-test.run", "^"+test+"$", "-test.count=1", //nolint:gosec // the binary this built
				"-test.timeout", timeout, "-test.coverprofile", profile)
			run.Env = testEnv()
			// A failure here is the package's own tests' to report, not this gate's; what this
			// gate does with it is ask the test about no edit, since it fails whatever the edit -
			// and say so, since an edit only it reaches then reads as one nothing holds.
			var (
				said     []byte
				exceeded bool
				runErr   error
				took     time.Duration
			)
			if err := tree.in(ctx, dir, func(at, tmp string) {
				run.Dir, run.Env = at, append(run.Env, "TMPDIR="+tmp)
				started := time.Now()
				said, exceeded, runErr = bounded("cover: run one test", run, nil)
				took = time.Since(started)
			}); err != nil {
				mu.Lock()
				treeErr = cmp.Or(treeErr, err)
				mu.Unlock()
				return
			}
			// Killed by the cancellation, a test reads as one that fails with nothing broken.
			if ctx.Err() != nil {
				return
			}
			if exceeded {
				runErr = fmt.Errorf("held over the %s a test process may, and stopped", bound())
			}
			failed := runErr != nil
			if failed {
				// mutate-exempt: how much of the failure is shown, not whether it is.
				shown := tail(said, 20)
				fmt.Fprintf(os.Stderr, "  (%s: %s fails with nothing broken, and is asked about no edit: %v)\n%s\n",
					dir, test, runErr, shown)
			}
			// Whether or not its profile can be read: a run of every test runs this one too.
			mu.Lock()
			c.took[test] = took
			mu.Unlock()
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
			maps.Copy(c.blocks, blocks)
			for file, s := range spans {
				if c.reached[file] == nil {
					c.reached[file] = map[string][]span{}
				}
				c.reached[file][test] = s
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if treeErr != nil {
		return nil, treeErr
	}
	return c, nil
}

// runByDefault is whether a run of a test binary with no flag but -test.run runs what -test.list
// names: a test, a fuzz target's seeds, an example. One left out of Cover is asked about no edit
// that coverage names tests for. A benchmark runs only under -test.bench.
func runByDefault(name string) bool {
	return strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Fuzz") || strings.HasPrefix(name, "Example")
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
		a, okA := placeIn(from)
		b, okB := placeIn(to)
		if !okA || !okB {
			continue
		}
		all[name] = append(all[name], span{a, b})
		if !strings.HasSuffix(rest, " 0") {
			executed[name] = append(executed[name], span{a, b})
		}
	}
	return executed, all, lines.Err()
}

// placeIn is the place a profile writes as line.column.
func placeIn(s string) (place, bool) {
	line, col, _ := strings.Cut(s, ".")
	l, errL := strconv.Atoi(line)
	c, errC := strconv.Atoi(col)
	return place{l, c}, errL == nil && errC == nil
}
