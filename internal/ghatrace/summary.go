package ghatrace

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

// WriteSummary writes t as Markdown for the run's Job Summary: what it cost, the critical path,
// the jobs as a Gantt chart, and the slowest steps and spans. files names where the HTML page and
// the Perfetto trace are, for the reader to open.
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
	if len(t.Lanes) > 0 {
		b.WriteString(gantt(t))
	}
	if rows := slowestOf(t, slowest); len(rows) > 0 {
		b.WriteString("\n| slowest | job | took |\n|---|---|---|\n")
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

// slowestOf is the n longest steps and spans of t, the longest first. GitHub's own setup and
// teardown count: a slow "Set up job" is a slow runner.
func slowestOf(t Trace, n int) []row {
	var rows []row
	for _, l := range t.Lanes {
		for _, s := range flatten(l.Job.Children) {
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

// gantt is the jobs as a Mermaid Gantt chart, which GitHub draws in a summary: each job's wait
// for a runner, then the job, red on the critical path. Steps are left to the HTML page: a
// chart with hundreds of bars is one nobody reads.
func gantt(t Trace) string {
	var b strings.Builder
	b.WriteString("```mermaid\ngantt\n    dateFormat x\n    axisFormat %H:%M\n")
	for i, l := range t.Lanes {
		fmt.Fprintf(&b, "    section %s\n", mermaid(l.Name))
		if l.Queue.Duration() >= time.Second {
			fmt.Fprintf(&b, "    wait %s :done, q%d, %d, %d\n", short(l.Queue.Duration()), i, l.Queue.Start.UnixMilli(), l.Queue.End.UnixMilli())
		}
		tag := "active"
		if l.Critical {
			tag = "crit"
		}
		name := short(l.Job.Duration())
		if l.Job.Failed {
			name = "failed " + name
		}
		// A bar shorter than a second draws as nothing, and a job that short still ran.
		end := max(l.Job.End.UnixMilli(), l.Job.Start.UnixMilli()+1000)
		fmt.Fprintf(&b, "    %s :%s, j%d, %d, %d\n", name, tag, i, l.Job.Start.UnixMilli(), end)
	}
	b.WriteString("```\n")
	return b.String()
}

// mermaid is a name a Gantt line can hold: a colon ends the name, a hash starts a comment, and a
// semicolon ends the line.
func mermaid(s string) string {
	return strings.NewReplacer(":", " ", "#", " ", ";", " ").Replace(s)
}

// cell is a name a Markdown table can hold.
func cell(s string) string {
	return strings.ReplaceAll(s, "|", `\|`)
}
