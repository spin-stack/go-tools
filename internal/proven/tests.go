package proven

import (
	"regexp"
	"strconv"
)

// Test is one test a job's log says ended, with its answer: a Go test by go test's own line for
// it, a Playwright test by its list reporter's.
type Test struct {
	// Name is the Go test's name, or the Playwright test's title: the last of its titles, since
	// the ones before it are its describe blocks'.
	Name string `json:"name"`
	// Spec is the file a Playwright test is in, by its name alone; empty for a Go test.
	Spec    string  `json:"spec,omitempty"`
	Passed  bool    `json:"passed"`
	Seconds float64 `json:"seconds"`
}

// goTest is go test's line for a top-level test that ended: a subtest's name has a slash in it
// and is its parent's business. What a CI log puts before it on the line - a timestamp - is let
// through.
var goTest = regexp.MustCompile(`--- (PASS|FAIL): (Test[A-Za-z0-9_]+) \(([0-9.]+)s\)`)

// pwTest is Playwright's list reporter's line for a test that ended, as it prints it with no
// terminal: the mark, the test's number, the project, the spec with its line and column, the
// titles joined by › (a describe's, then the test's), a retry's suffix, and how long it took.
var pwTest = regexp.MustCompile(`([✓✘])\s+\d+\s+(?:\[[^\]]+\] › )?([^\s›]+\.[a-z]+):\d+:\d+ › (.+?)(?: \(retry #\d+\))? \(([0-9.]+)(ms|s|m)\)\s*$`)

// lastTitle is the test's own title: what follows the last › of the titles.
var lastTitle = regexp.MustCompile(`^(?:.* › )?(.+)$`)

// Tests is every test a log says ended, in the order they did, each with whether it passed and
// how long it took. A test run again on a retry is there once per answer.
func Tests(log string) []Test {
	var out []Test
	for _, m := range goTest.FindAllStringSubmatch(log, -1) { // mutate-exempt: any n < 0 is every match
		seconds, _ := strconv.ParseFloat(m[3], 64) // the expression matched digits and dots; a second dot is 0, which is the log's mistake to show
		out = append(out, Test{Name: m[2], Passed: m[1] == "PASS", Seconds: seconds})
	}
	for _, line := range regexp.MustCompile(`\r?\n`).Split(log, -1) { // mutate-exempt: any n < 0 is every line
		m := pwTest.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, _ := strconv.ParseFloat(m[4], 64) // mutate-exempt: any bit size but 32 is 64; the digits and dots matched, as above
		switch m[5] {
		case "ms":
			n /= 1000
		case "m":
			n *= 60
		}
		out = append(out, Test{Name: lastTitle.FindStringSubmatch(m[3])[1], Spec: m[2], Passed: m[1] == "✓", Seconds: n})
	}
	return out
}
