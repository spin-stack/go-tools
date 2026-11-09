package ghatrace

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"time"
)

// Span is a segment a job recorded inside one of its steps: a command `ghatrace span` ran.
// Runner and Job are how the run finds the job it belongs to: the API names each job's runner, and
// a runner runs one job at a time.
type Span struct {
	Name   string    `json:"name"`
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Exit   int       `json:"exit"`
	Runner string    `json:"runner"`
	Job    string    `json:"job"`
}

// Append writes s as one line to the file at path. Lines are short and written with one call to
// a file opened to append, so spans that end at once in one job do not interleave.
func Append(path string, s Span) error {
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
// a span file is written by Append and nothing else.
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

// place puts each span under the step it ran in, in the lane whose runner recorded it while its
// job ran, and nests spans inside the spans that contain them.
func place(lanes []Lane, spans []Span) {
	slices.SortStableFunc(spans, func(a, b Span) int { return outerFirst(a.Start, a.End, b.Start, b.End) })
	for _, s := range spans {
		l := laneOf(lanes, s)
		if l == nil {
			continue
		}
		slice := Slice{Name: s.Name, Kind: "span", Start: s.Start, End: s.End, Failed: s.Exit != 0}
		parent := &l.Job
		// Steps are timed to the second, floored: the step a span ran in is the last to start by then.
		for i := range l.Job.Children {
			if !l.Job.Children[i].Start.After(s.Start) {
				parent = &l.Job.Children[i]
			}
		}
		nest(parent, slice)
	}
}

func laneOf(lanes []Lane, s Span) *Lane {
	for i := range lanes {
		l := &lanes[i]
		if l.Runner == s.Runner && !s.Start.Before(l.Job.Start.Add(-slack)) && !s.Start.After(l.Job.End.Add(slack)) {
			return l
		}
	}
	return nil
}

// outerFirst orders spans by start, the longer first, so a span comes after any that contains it.
func outerFirst(aStart, aEnd, bStart, bEnd time.Time) int {
	return cmp.Or(aStart.Compare(bStart), bEnd.Compare(aEnd))
}

// nest adds s under the deepest span of parent that contains it. Spans arrive by start, the longer
// first, so a span that contains another is already there.
func nest(parent *Slice, s Slice) {
	for {
		var inside *Slice
		for i := range parent.Children {
			c := &parent.Children[i]
			if !s.Start.Before(c.Start) && !s.End.After(c.End) {
				inside = c
			}
		}
		if inside == nil {
			parent.Children = append(parent.Children, s)
			return
		}
		parent = inside
	}
}
