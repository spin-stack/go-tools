// Package ghatrace is the record of a span: a part of a GitHub Actions step that `go tool ghatrace
// run` draws under the step it ran in. `ghatrace span -- cmd` records one around a command; a
// program that knows its own parts - a lane runner, a test harness - records them with Record.
package ghatrace

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Span is one part of a step. Runner and Job are how the run finds the job it belongs to: the API
// names each job's runner, and a runner runs one job at a time.
type Span struct {
	Name   string    `json:"name"`
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Exit   int       `json:"exit"`
	Runner string    `json:"runner"`
	Job    string    `json:"job"`
	// Parent is the name of the span this one is part of, when spans ran side by side: by the time
	// alone, a part of one is inside the other too. Empty, the span goes under the innermost span
	// whose time contains it.
	Parent string `json:"parent,omitempty"`
}

// File is where spans are recorded: $GHATRACE_SPANS, or a file of the runner's own on one
// ($RUNNER_TEMP/ghatrace-spans.jsonl, which a job uploads as an artifact named ghatrace-*), and
// "" off one.
func File() string {
	if path := os.Getenv("GHATRACE_SPANS"); path != "" {
		return path
	}
	if tmp := os.Getenv("RUNNER_TEMP"); tmp != "" {
		return filepath.Join(tmp, "ghatrace-spans.jsonl")
	}
	return ""
}

// Record adds s to File, with this job's runner and name when s has none, and does nothing off a
// runner. A line is short and written with one call to a file opened to append, so spans that end
// at once in one job do not interleave.
func Record(s Span) error {
	path := File()
	if path == "" {
		return nil
	}
	if s.Runner == "" {
		s.Runner = os.Getenv("RUNNER_NAME")
	}
	if s.Job == "" {
		s.Job = os.Getenv("GITHUB_JOB")
	}
	line, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // a span file is read by whoever reads the run
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return errors.Join(err, f.Close())
}

// Read is every span in r, one JSON object a line. A line that is not one is an error naming it:
// a span file is written by Record and nothing else.
func Read(r io.Reader) ([]Span, error) {
	var spans []Span
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var s Span
		if err := json.Unmarshal(sc.Bytes(), &s); err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		spans = append(spans, s)
	}
	return spans, sc.Err()
}
