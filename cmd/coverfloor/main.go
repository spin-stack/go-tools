// Command coverfloor holds `task test`'s coverage profile to a floor, counting only what CI can
// run.
//
//	coverfloor FLOOR coverage.out
//
// It runs from the module's root, which the profile's files are in. A function whose doc comment
// says `mutate-lane: TestName` is held by a lane's test, one that needs root, KVM or a cloud
// account, which a CI runner has none of; mutate makes no edit in it, and the floor leaves it out
// for the same reason, read by the same code (mutate.LaneFunctions). Counted, every probe
// spin-machine's lab gained pulled its total down however well the testable half was tested.
//
// The comparison is on the exact percentage. `go tool cover -func` rounds to one decimal, and read
// spin-machine's 79.96% as 80.0, which passed a floor of 80.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spin-stack/go-tools/internal/mutate"
)

func main() {
	os.Exit(run(os.Args[1:], ".", os.Stdout, os.Stderr)) // mutate-exempt: the process's own arguments; run is what the tests drive
}

// run is the command on args, a floor and a profile, in the module at dir: 0 at or above the
// floor, 1 below it, 2 when it could not tell.
func run(args []string, dir string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		_, _ = fmt.Fprintln(stderr, "usage: coverfloor FLOOR coverage.out")
		return 2
	}
	floor, err := strconv.ParseFloat(args[0], 64) // mutate-exempt: ParseFloat reads every bitSize but 32 as 64
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "coverfloor: the floor: %v\n", err)
		return 2
	}
	t, err := measure(args[1], dir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "coverfloor: %v\n", err)
		return 2
	}
	got := t.percent(false)
	if got < floor {
		_, _ = fmt.Fprintf(stdout, "coverage is %.2f%% of what CI runs, below the floor of %g%%\n", got, floor)
		return 1
	}
	if t.laneStatements == 0 {
		_, _ = fmt.Fprintf(stdout, "coverage: %.2f%% of what CI runs (floor %g%%)\n", got, floor)
		return 0
	}
	_, _ = fmt.Fprintf(stdout, "coverage: %.2f%% of what CI runs (floor %g%%); %.2f%% with the %d statements only a lane runs\n",
		got, floor, t.percent(true), t.laneStatements)
	return 0
}

// total is a profile's statements: all of them, those covered, and how many of each are in
// functions a lane holds.
type total struct {
	statements, covered         int
	laneStatements, laneCovered int
}

// percent is the covered share of the statements, with the lane's or without them.
func (t total) percent(withLane bool) float64 {
	n, c := t.statements-t.laneStatements, t.covered-t.laneCovered
	if withLane {
		n, c = t.statements, t.covered
	}
	if n == 0 {
		return 0
	}
	return 100 * float64(c) / float64(n)
}

// block is one line of a coverage profile.
type block struct {
	file       string // as the profile names it: the import path, then the file
	from, to   int    // lines
	statements int
	covered    bool
	key        string // file:range, which a block repeated across test binaries shares
}

// measure reads the profile at path, whose files are under the module at dir.
func measure(path, dir string) (total, error) {
	module, err := modulePath(dir)
	if err != nil {
		return total{}, err
	}
	blocks, err := readProfile(path)
	if err != nil {
		return total{}, err
	}
	lanes := map[string][][2]int{} // a file's lane functions, by their first and last lines
	var t total
	for _, b := range blocks {
		ranges, ok := lanes[b.file]
		if !ok {
			rel, found := strings.CutPrefix(b.file, module+"/")
			if !found {
				return total{}, fmt.Errorf("reading %s: %s is not in module %s", path, b.file, module)
			}
			if ranges, err = mutate.LaneFunctions(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
				return total{}, err
			}
			lanes[b.file] = ranges
		}
		inLane := false
		for _, r := range ranges {
			if b.from >= r[0] && b.to <= r[1] {
				inLane = true
				break
			}
		}
		t.statements += b.statements
		if inLane {
			t.laneStatements += b.statements
		}
		if b.covered {
			t.covered += b.statements
			if inLane {
				t.laneCovered += b.statements
			}
		}
	}
	return t, nil
}

// readProfile is the profile's blocks, each once: covered if any test binary ran it, as `go tool
// cover` merges them.
func readProfile(path string) ([]block, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("reading the coverage profile: %w", err)
	}
	defer func() { _ = f.Close() }()
	var blocks []block
	seen := map[string]int{}
	s := bufio.NewScanner(f)
	for n := 1; s.Scan(); n++ {
		line := s.Text()
		if n == 1 {
			if !strings.HasPrefix(line, "mode: ") {
				return nil, fmt.Errorf("reading %s: line 1 is %q, not a coverage profile's mode", path, line)
			}
			continue
		}
		b, err := parseBlock(line)
		if err != nil {
			return nil, fmt.Errorf("reading %s, line %d: %w", path, n, err)
		}
		if i, ok := seen[b.key]; ok {
			blocks[i].covered = blocks[i].covered || b.covered
			continue
		}
		seen[b.key] = len(blocks)
		blocks = append(blocks, b)
	}
	if err := s.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("reading %s: no blocks, so no tests ran", path)
	}
	return blocks, nil
}

// parseBlock reads "file.go:12.3,14.2 2 1": a range of lines and columns, its statements, and
// how many times they ran.
func parseBlock(line string) (block, error) {
	fields := strings.Fields(line)
	if len(fields) != 3 {
		return block{}, fmt.Errorf("%q is not file:range statements count", line)
	}
	// A missing ":" or "," leaves a position empty, which lineOf refuses.
	file, span, _ := strings.Cut(fields[0], ":")
	start, end, _ := strings.Cut(span, ",")
	from, err1 := lineOf(start)
	to, err2 := lineOf(end)
	statements, err3 := strconv.Atoi(fields[1])
	count, err4 := strconv.Atoi(fields[2])
	for _, err := range []error{err1, err2, err3, err4} {
		if err != nil {
			return block{}, fmt.Errorf("%q: %w", line, err)
		}
	}
	return block{file: file, from: from, to: to, statements: statements, covered: count > 0, key: fields[0]}, nil
}

// lineOf is the line of "line.column".
func lineOf(position string) (int, error) {
	l, _, _ := strings.Cut(position, ".")
	return strconv.Atoi(l)
}

// modulePath is the module go.mod at dir declares.
func modulePath(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("reading the module's path: %w", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if m, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(m), nil
		}
	}
	return "", fmt.Errorf("%s declares no module", filepath.Join(dir, "go.mod"))
}
