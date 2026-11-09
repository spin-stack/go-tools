package runtrace

import (
	"cmp"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"time"
)

// stats is what a run cost and where it waited.
type stats struct {
	Wall time.Duration
	// Runner is every job's time on its runner, summed; Minutes is the same with each job rounded
	// up to the minute, as GitHub bills its own runners.
	Runner  time.Duration
	Minutes int
	// Queued is every job's wait for a runner, summed, and the longest one.
	Queued    time.Duration
	Worst     time.Duration
	WorstLane string
}

// measure is t's stats.
func measure(t Trace) stats {
	st := stats{Wall: t.Wall()}
	for _, l := range t.Lanes {
		d := l.Job.Duration()
		st.Runner += d
		st.Minutes += int(math.Ceil(d.Minutes()))
		q := l.Queue.Duration()
		st.Queued += q
		if q > st.Worst {
			st.Worst, st.WorstLane = q, l.Name
		}
	}
	return st
}

// slowest is how many steps and spans the summary lists.
const slowest = 10

// WriteSummary writes t as Markdown for the run's Job Summary: what it cost, the critical path and
// the slowest steps and spans. The waterfall is the HTML page: files names where it and the
// Perfetto trace are, for the reader to open.
func WriteSummary(w io.Writer, t Trace, files string) error {
	st := measure(t)
	var b strings.Builder
	fmt.Fprintf(&b, "## %s: where the time went\n\n", t.Title)
	fmt.Fprintf(&b, "| wall | on runners | waiting for runners | longest wait |\n|---|---|---|---|\n")
	fmt.Fprintf(&b, "| **%s** | %s (%d job-minutes rounded up) | %s | %s |\n\n",
		short(st.Wall), short(st.Runner), st.Minutes, short(st.Queued), worst(st))
	var path []string
	for _, l := range t.Lanes {
		if l.Critical {
			path = append(path, fmt.Sprintf("%s (%s, waited %s)", l.Name, short(l.Job.Duration()), short(l.Queue.Duration())))
		}
	}
	if len(path) > 0 {
		fmt.Fprintf(&b, "**Critical path**: %s\n\n", strings.Join(path, " → "))
	}
	if rows := slowestOf(t, slowest); len(rows) > 0 {
		b.WriteString("| slowest | job | took |\n|---|---|---|\n")
		for _, r := range rows {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", cell(r.Name), cell(r.Lane), short(r.Took))
		}
	}
	if len(t.Skipped) > 0 {
		fmt.Fprintf(&b, "\nDid not run: %s.\n", strings.Join(t.Skipped, ", "))
	}
	if files != "" {
		fmt.Fprintf(&b, "\n%s\n", files)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func worst(st stats) string {
	if st.WorstLane == "" {
		return "-"
	}
	return short(st.Worst) + " (" + st.WorstLane + ")"
}

// row is a step or a span in the slowest list.
type row struct {
	Name string
	Lane string
	Took time.Duration
}

// slowestOf is the n longest steps and spans of t that hold no other, the longest first: what
// holds others says again what they say, and what to make faster is what is inside. GitHub's own
// setup and teardown count: a slow "Set up job" is a slow runner.
func slowestOf(t Trace, n int) []row {
	var rows []row
	for _, l := range t.Lanes {
		for _, s := range flatten(l.Job.Children) {
			if len(s.Children) > 0 {
				continue
			}
			name := s.Name
			if s.Kind == "span" {
				name += " (span)"
			}
			rows = append(rows, row{Name: name, Lane: l.Name, Took: s.Duration()})
		}
	}
	slices.SortStableFunc(rows, func(a, b row) int { return cmp.Compare(b.Took, a.Took) })
	return rows[:min(n, len(rows))]
}

// cell is a name a Markdown table can hold.
func cell(s string) string {
	return strings.ReplaceAll(s, "|", `\|`)
}
