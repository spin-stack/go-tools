// Package ghatrace is the record of a span: a part of a GitHub Actions step that `go tool ghatrace
// run` draws under the step it ran in. `ghatrace span -- cmd` records one around a command; a
// program that knows its own parts - a lane runner, a test harness - records them with Record.
// The same file holds the job's machine, sampled by `ghatrace sample` (Sample).
package ghatrace

import (
	"bufio"
	"cmp"
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
	// Usage is what the span's command used, when the span ran one: ghatrace span's and gotest's.
	Usage *Usage `json:"usage,omitempty"`
}

// Usage is what a command used, as the kernel counts a process and every descendant it waited
// for. What one starts and leaves running is not in it: a daemon, or a container's processes,
// which are the container runtime's children and not docker run's.
type Usage struct {
	User   time.Duration `json:"user_ns"`
	System time.Duration `json:"system_ns"`
	// MaxRSS is the largest resident set of any one of those processes, not their sum.
	MaxRSS int64 `json:"max_rss_bytes,omitempty"`
	// Read is what they read from a disk, without what the page cache held; Written is what they
	// wrote toward one, counted as it reached the page cache. A file on tmpfs is neither.
	Read    int64 `json:"read_bytes,omitempty"`
	Written int64 `json:"written_bytes,omitempty"`
}

// CPU is the time the command ran on a CPU, its own and the kernel's for it.
func (u Usage) CPU() time.Duration { return u.User + u.System }

// UsageOf is what the process p describes used: its CPU time everywhere, and on Linux its largest
// resident set and the bytes it moved to and from a disk. Nil for a process that never ran.
func UsageOf(p *os.ProcessState) *Usage {
	if p == nil {
		return nil
	}
	u := &Usage{User: p.UserTime(), System: p.SystemTime()}
	rusage(p, u)
	return u
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
	s.Runner = cmp.Or(s.Runner, os.Getenv("RUNNER_NAME"))
	s.Job = cmp.Or(s.Job, os.Getenv("GITHUB_JOB"))
	return add(s)
}

// RecordSample adds s to File as Record adds a span, under the key "sample": a reader that knows
// only spans, a ghatrace from before samples, reads the line as a span of no runner, which no job
// ran, and draws nothing.
func RecordSample(s Sample) error {
	s.Runner = cmp.Or(s.Runner, os.Getenv("RUNNER_NAME"))
	s.Job = cmp.Or(s.Job, os.Getenv("GITHUB_JOB"))
	return add(line{Sample: &s})
}

// line is one line of the file: a span, or a sample under its own key.
type line struct {
	Span
	Sample *Sample `json:"sample,omitempty"`
}

func add(v any) error {
	path := File()
	if path == "" {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // a span file is read by whoever reads the run
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return errors.Join(err, f.Close())
}

// Read is every span in r, one JSON object a line, without the samples among them. A line that is
// not one is an error naming it: a span file is written by Record and RecordSample and nothing else.
func Read(r io.Reader) ([]Span, error) {
	spans, _, err := ReadAll(r)
	return spans, err
}

// ReadAll is every span and every sample in r.
func ReadAll(r io.Reader) ([]Span, []Sample, error) {
	var spans []Span
	var samples []Sample
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var l line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			return nil, nil, fmt.Errorf("line %d: %w", n, err)
		}
		if l.Sample != nil {
			samples = append(samples, *l.Sample)
			continue
		}
		spans = append(spans, l.Span)
	}
	return spans, samples, sc.Err()
}
