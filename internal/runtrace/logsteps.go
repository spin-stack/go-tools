package runtrace

import (
	"strings"
	"time"
)

// group is a `##[group]Run ...` line of a job's log: where GitHub starts each step it runs, a
// composite action's own steps included, which the jobs API does not list.
type group struct {
	name string
	at   time.Time
	// next is when the group after it started: zero for the log's last.
	next time.Time
}

// groups is every step start in log, in order. Each line of a job's log starts with its time.
func groups(log string) []group {
	var out []group
	for line := range strings.Lines(log) {
		stamp, text, ok := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
		if !ok {
			continue
		}
		name, ok := strings.CutPrefix(text, "##[group]Run ")
		if !ok {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, strings.TrimPrefix(stamp, "\ufeff"))
		if err != nil {
			continue
		}
		if len(out) > 0 { // mutate-exempt: the log's first group is the first of its step, the step itself, whose next nothing reads
			out[len(out)-1].next = at
		}
		out = append(out, group{name: name, at: at})
	}
	return out
}

// subSteps puts under each of job's steps the steps it ran itself, from its log: a composite
// action's. A step's first group is the step; the ones after it, until the next step starts, are
// its own, each until the next group or the step's end.
func subSteps(job *Slice, log string) {
	byStep := make([][]group, len(job.Children))
	for _, g := range groups(log) {
		// Steps are timed to the second, floored, and in order: a line is the last step's to start
		// by then.
		started := 0
		for _, s := range job.Children {
			if !s.Start.After(g.at) {
				started++
			}
		}
		if started > 0 {
			byStep[started-1] = append(byStep[started-1], g)
		}
	}
	for s, own := range byStep {
		if len(own) < 2 {
			continue
		}
		parent := &job.Children[s]
		// A step's end is floored too: its last part ends within the second after it.
		end := parent.End.Add(slack)
		for _, g := range own[1:] {
			stop := end
			if !g.next.IsZero() && g.next.Before(stop) {
				stop = g.next
			}
			parent.Children = append(parent.Children, Slice{Name: g.name, Kind: "step", Start: g.at, End: stop})
		}
	}
}
