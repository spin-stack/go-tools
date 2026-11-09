package runtrace

import (
	"cmp"
	"slices"
	"time"

	"github.com/spin-stack/go-tools/ghatrace"
)

// place puts each span under the step it ran in, in the lane whose runner recorded it while its
// job ran, and nests spans inside the spans that contain them.
func place(lanes []Lane, spans []ghatrace.Span) {
	slices.SortStableFunc(spans, func(a, b ghatrace.Span) int { return outerFirst(a.Start, a.End, b.Start, b.End) })
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

func laneOf(lanes []Lane, s ghatrace.Span) *Lane {
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
