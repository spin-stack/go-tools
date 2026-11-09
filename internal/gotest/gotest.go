// Package gotest reads `go test -json` as it comes: it writes what go test would have written
// without -json, and records a span for each package and for each test that took long enough to
// be worth a bar - a test under its package, a subtest under its test.
package gotest

import (
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/spin-stack/go-tools/ghatrace"
)

// event is one line of `go test -json` (go doc test2json).
type event struct {
	Time    time.Time `json:"Time"`
	Action  string    `json:"Action"`
	Package string    `json:"Package"`
	Test    string    `json:"Test"`
	Output  string    `json:"Output"`
}

// Reader turns go test's JSON back into its text and its tests into spans.
type Reader struct {
	Out io.Writer
	// Verbose is whether go test was asked for -v: with -json it writes every line as -v would,
	// and without -v a passing test says nothing.
	Verbose bool
	// Min is the shortest test that is a span; a package always is.
	Min time.Duration
	// Parent names the span the packages are part of: packages run side by side, and by time
	// alone one would be drawn inside another.
	Parent string
	Record func(ghatrace.Span)

	started map[string]time.Time // by package and test
	held    map[string][]string  // a test's output, until it fails or passes
}

func key(pkg, test string) string { return pkg + "\x00" + test }

// Line is one line of go test's output. A line that is not an event - what a wrapper in front of
// go test printed - is written as it is.
func (r *Reader) Line(line []byte) error {
	var e event
	if err := json.Unmarshal(line, &e); err != nil || e.Action == "" {
		_, err := r.Out.Write(append(line, '\n'))
		return err
	}
	if r.started == nil {
		r.started, r.held = map[string]time.Time{}, map[string][]string{}
	}
	k := key(e.Package, e.Test)
	switch e.Action {
	case "start", "run":
		r.started[k] = e.Time
	case "output", "build-output":
		return r.output(e, k)
	case "pass", "fail", "skip":
		return r.end(e, k)
	}
	return nil
}

func (r *Reader) output(e event, k string) error {
	switch {
	case r.Verbose:
	case e.Test != "":
		// Without -v, a test's lines are said only when it fails, and go test's own markers
		// never are.
		if !strings.HasPrefix(e.Output, "=== ") {
			r.held[k] = append(r.held[k], e.Output)
		}
		return nil
	case e.Output == "PASS\n":
		// The test binary's own verdict, which go test without -v leaves to its "ok" line.
		return nil
	}
	_, err := io.WriteString(r.Out, e.Output)
	return err
}

func (r *Reader) end(e event, k string) error {
	held := r.held[k]
	delete(r.held, k)
	start, ok := r.started[k]
	delete(r.started, k)
	if e.Action == "fail" {
		for _, line := range held {
			if _, err := io.WriteString(r.Out, line); err != nil {
				return err
			}
		}
	}
	if !ok || r.Record == nil {
		return nil
	}
	s := ghatrace.Span{Start: start, End: e.Time}
	if e.Action == "fail" {
		s.Exit = 1
	}
	switch {
	case e.Test == "" && e.Action == "skip":
		// A package with no tests: nothing ran.
		return nil
	case e.Test == "":
		s.Name, s.Parent = Short(e.Package), r.Parent
	case s.End.Sub(s.Start) < r.Min:
		return nil
	case strings.Contains(e.Test, "/"):
		s.Name, s.Parent = e.Test, e.Test[:strings.LastIndex(e.Test, "/")]
	default:
		s.Name, s.Parent = e.Test, Short(e.Package)
	}
	r.Record(s)
	return nil
}

// Short is a package's path without its module's host, owner and name, when it has them - the
// part a reader of one repository's trace tells packages apart by.
func Short(pkg string) string {
	parts := strings.SplitN(pkg, "/", 4)
	if len(parts) == 4 && strings.Contains(parts[0], ".") {
		return parts[3]
	}
	return pkg
}
