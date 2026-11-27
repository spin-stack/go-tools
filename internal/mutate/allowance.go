package mutate

import (
	"bytes"
	"context"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// An edit that makes a test wait for ever - a wait loop's exit inverted, a row lock never let go,
// a channel never closed - holds its test process asleep, at no CPU, until something stops it. One
// allowance for every run stopped it at -timeout, which has to be what a package's slowest whole
// run takes on a loaded machine: spin's checks lane sets it at fifteen minutes, and an edit whose
// test takes seconds held a slot for fifteen. So a run, and each test in it, is allowed a multiple
// of what it took with nothing broken, as this run measured it, and a margin: the rule mutation
// tools keep (PIT's factor × normal + constant). -timeout stays what no run may pass.
//
// Each test as well as the run: an edit asks every test of an importer, and of its own package
// where coverage cannot say which reach it, and a package's tests together take minutes, which a
// multiple of is no stop at all for the one test that hangs.
// slack is how many times its unbroken time a test, or a run, may take with an edit in.
const slack = 4

// margin is what each may take beyond that: a process's start, and a test of milliseconds that
// waits on the run's one PostgreSQL behind the other jobs. A variable, as memLimit is, so that a
// test showing a stop waits a second for it and not ten, in every run of this gate that asks it.
var margin = 10 * time.Second

// Unbroken is how long tests took with nothing broken, by the absolute directory of their package:
// each test - a package's own each alone (Cover), an importer's as its run said - and an importer's
// tests together (brokenImporters). Measured before any edit of the package is asked.
type Unbroken struct {
	mu    sync.Mutex
	tests map[string]map[string]time.Duration
	whole map[string]time.Duration
}

// NewUnbroken is an Unbroken with nothing measured: every run is allowed -timeout.
func NewUnbroken() *Unbroken {
	return &Unbroken{tests: map[string]map[string]time.Duration{}, whole: map[string]time.Duration{}}
}

func (u *Unbroken) recordTests(dir string, took map[string]time.Duration) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.tests[absDir(dir)] = took
}

func (u *Unbroken) recordWhole(dir string, took time.Duration) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.whole[absDir(dir)] = took
}

// Allowance is how long one run of tests may take, and each test in it from when it starts.
type Allowance struct {
	// Run is 0 for no bound.
	Run time.Duration
	// Each is a test's own; a test with none has only the run's.
	Each map[string]time.Duration
}

// allowance is what a run of the tests of the package in dir may take with an edit in: those of
// only, or all where only is nil. The run is allowed ceiling where any of them was not measured,
// and never more; a ceiling of 0 is none, as -test.timeout 0 is.
func (u *Unbroken) allowance(dir string, only []string, ceiling time.Duration) Allowance {
	a := Allowance{Run: ceiling, Each: map[string]time.Duration{}}
	if u == nil {
		return a
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	dir = absDir(dir)
	tests := u.tests[dir]
	for test, took := range tests {
		a.Each[test] = slack*took + margin
	}
	run, ok := u.whole[dir]
	if only != nil || !ok {
		run, ok = sum(tests, only)
	}
	switch {
	case ok && ceiling > 0: // mutate-exempt: a ceiling of a nanosecond is one nobody sets; 0 is none
		a.Run = min(slack*run+margin, ceiling)
	case ok:
		a.Run = slack*run + margin
	}
	return a
}

// sum is the time of the tests of only, or all where only is nil; false where one was not
// measured, or none was: a package with only examples or fuzz targets has none, and a run of it is
// theirs. A process running them one after another is no slower than each in one of its own.
func sum(tests map[string]time.Duration, only []string) (total time.Duration, measured bool) {
	if only == nil {
		only = slices.Collect(maps.Keys(tests))
	}
	for _, test := range only {
		took, ok := tests[test]
		if !ok {
			return total, false
		}
		total += took
	}
	return total, len(tests) > 0
}

func absDir(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}

// A top-level test's start, pause and end as -test.v says them; where in a line is framed's. A
// subtest's name has a /.
var (
	testStart = regexp.MustCompile(`=== RUN   ((?:Test|Fuzz|Example)[^\s/]*)$`)
	testPause = regexp.MustCompile(`=== PAUSE ((?:Test|Fuzz|Example)[^\s/]*)$`)
	testOver  = regexp.MustCompile(`--- (?:PASS|FAIL|SKIP): ((?:Test|Fuzz|Example)[^\s/]*) \(`)
)

// framed is the test marker names in line, where the test binary said it: at the line's start, or
// straight after what a test printed with no newline, which comes first. Never after indentation: a
// test's own output saying another binary's -test.v - a t.Log of a go test run inside it - is
// indented, or follows its file and line. A start also never follows text that ends in a space,
// where a false one times a test that is not running. An end or a pause may, because the binary
// appends it to a line a test left open with a trailing space, and a false one only stops timing
// a test, which leaves the run's own bound.
func framed(marker *regexp.Regexp, line string, afterSpace bool) string {
	m := marker.FindStringSubmatchIndex(line)
	if m == nil {
		return ""
	}
	if before := line[:m[0]]; before != "" {
		if strings.Trim(before, " \t") == "" || !afterSpace && strings.ContainsRune(" \t", rune(before[len(before)-1])) {
			return ""
		}
	}
	return line[m[2]:m[3]]
}

// keep is how much of a line the pacer holds while it waits for the line's end: a marker and a
// test's name, which is all it reads, and room to spare. A test that prints with no newline - a
// dot a write, for minutes - would otherwise be held whole, and scanned again on every write the
// test waits on. It holds at most twice keep, and a line longer than that is read by its last keep
// bytes: what it loses is a test whose name is most of them.
const keep = 4096

// pacer stops a test binary once a test runs past its own allowance, reading what the binary says
// as it says it. A test is timed from its start until it ends or pauses: a parallel test pauses
// and is let go with the others once every test has run its part one at a time, and then each is
// slower by the others, so only the run's allowance holds.
type pacer struct {
	each map[string]time.Duration
	stop context.CancelFunc

	mu      sync.Mutex
	partial []byte
	// running counts the starts and ends read, so the timer of a test that has ended stops nothing.
	running int
	// ranPast is the test stopped, with its allowance.
	ranPast string
	allowed time.Duration
}

func (p *pacer) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(b)
	for {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			p.hold(b)
			return n, nil
		}
		p.hold(b[:i])
		p.line(strings.TrimRight(string(p.partial), "\r"))
		p.partial = p.partial[:0]
		b = b[i+1:]
	}
}

// hold adds b to the line read so far, keeping its last keep bytes once it holds twice that.
func (p *pacer) hold(b []byte) {
	p.partial = append(p.partial, b...)
	if len(p.partial) > 2*keep {
		p.partial = p.partial[len(p.partial)-keep:]
	}
}

func (p *pacer) line(line string) {
	if framed(testPause, line, true) != "" || framed(testOver, line, true) != "" {
		p.running++
		return
	}
	test := framed(testStart, line, false)
	if test == "" {
		return
	}
	p.running++
	allowed, ok := p.each[test]
	if !ok {
		return
	}
	running := p.running
	time.AfterFunc(allowed, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.running == running {
			p.ranPast, p.allowed = test, allowed
			p.stop()
		}
	})
}

// stopped is the test that ran past its allowance, and the allowance; empty where none did.
func (p *pacer) stopped() (string, time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running++
	return p.ranPast, p.allowed
}
