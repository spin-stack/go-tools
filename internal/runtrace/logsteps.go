package runtrace

import (
	"slices"
	"strings"
	"time"
)

// actions is every step the job's log says a step ran itself: the runner writes a
// `##[start-action display=<name>;id=<id>]` line when a composite action starts one of its own
// steps, and `##[end-action id=<id>;...;conclusion=<conclusion>;...]` when it ends, each line
// starting with its time. The jobs API lists none of them.
func actions(log string) []Slice {
	type open struct {
		name string
		at   time.Time
	}
	started := map[string]open{}
	var out []Slice
	for line := range strings.Lines(log) {
		stamp, text, ok := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
		if !ok {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, strings.TrimPrefix(stamp, "\ufeff"))
		if err != nil {
			continue
		}
		if rest, ok := strings.CutPrefix(text, "##[start-action display="); ok {
			// The name is what the workflow wrote, a semicolon and all: the id is after the last.
			if i := strings.LastIndex(rest, ";id="); i >= 0 { // mutate-exempt: a display is never empty - the runner writes the step's name, or what it uses or runs
				started[strings.TrimSuffix(rest[i+len(";id="):], "]")] = open{name: rest[:i], at: at}
			}
			continue
		}
		rest, ok := strings.CutPrefix(text, "##[end-action id=")
		if !ok {
			continue
		}
		id, fields, _ := strings.Cut(strings.TrimSuffix(rest, "]"), ";")
		if s, ok := started[id]; ok {
			delete(started, id)
			failed := strings.Contains(";"+fields+";", ";conclusion=failure;")
			out = append(out, Slice{Name: s.name, Kind: "step", Start: s.at, End: at, Failed: failed})
		}
	}
	return out
}

// subSteps puts each step a step ran itself, from the job's log, under the step that ran it - by
// time, as a span is - and inside any of them that contains it: a composite action in another.
func subSteps(job *Slice, log string) {
	parts := actions(log)
	// An action that contains another, read first.
	slices.SortStableFunc(parts, func(a, b Slice) int { return outerFirst(a.Start, a.End, b.Start, b.End) })
	for _, p := range parts {
		parent := stepAt(job, p.Start)
		if parent == nil {
			continue
		}
		nest(parent, p)
	}
}

// stepAt is job's step that was running at t. Steps are timed to the second, floored, and in
// order: it is the last to start by then.
func stepAt(job *Slice, t time.Time) *Slice {
	var at *Slice
	for i := range job.Children {
		if !job.Children[i].Start.After(t) {
			at = &job.Children[i]
		}
	}
	return at
}
