// Package runtrace turns one workflow run - its jobs, their steps, and the spans its jobs recorded -
// into a trace: a lane per job with the wait for a runner, the job, its steps and the spans inside
// them, and the chain of jobs the run's wall time is made of.
package runtrace

import (
	"cmp"
	"slices"
	"time"

	"github.com/spin-stack/go-tools/ghatrace"
	"github.com/spin-stack/go-tools/internal/proven"
)

// Run is what the trace is of: the run's identity and every job of its latest attempt.
type Run struct {
	ID       int64
	Attempt  int
	Workflow string
	URL      string
	Jobs     []proven.Job
	// Now is the end of whatever has not finished: the job that writes the trace is still running.
	Now time.Time
}

// Slice is one bar: a wait, a job, a step or a span.
type Slice struct {
	Name     string    `json:"name"`
	Kind     string    `json:"kind"` // queue, job, step, span
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Failed   bool      `json:"failed,omitempty"`
	Children []Slice   `json:"children,omitempty"`
}

// Duration is the bar's length.
func (s Slice) Duration() time.Duration { return s.End.Sub(s.Start) }

// Lane is one job: its wait for a runner, then the job with its steps, and its steps' spans.
type Lane struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	URL        string   `json:"url"`
	Runner     string   `json:"runner"`
	Labels     []string `json:"labels"`
	Conclusion string   `json:"conclusion"`
	Queue      Slice    `json:"queue"`
	Job        Slice    `json:"job"`
	Critical   bool     `json:"critical"`
}

// Trace is a run as lanes, in the order their jobs were queued.
type Trace struct {
	Run   int64     `json:"run"`
	Title string    `json:"title"`
	URL   string    `json:"url"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Lanes []Lane    `json:"lanes"`
	// Skipped is the jobs that never ran: a condition said no, or a job they need failed.
	Skipped []string `json:"skipped"`
}

// Wall is the run from the first job queued to the last job finished.
func (t Trace) Wall() time.Duration { return t.End.Sub(t.Start) }

// Build is the trace of run, with spans placed under the step of the job that recorded them.
// A span no job ran is dropped: it was recorded by a runner the run does not name.
func Build(run Run, spans []ghatrace.Span) Trace {
	t := Trace{Run: run.ID, Title: run.Workflow, URL: run.URL}
	for _, j := range run.Jobs {
		if j.StartedAt.IsZero() || j.Conclusion == "skipped" {
			t.Skipped = append(t.Skipped, j.Name)
			continue
		}
		end := orNow(j.CompletedAt, run.Now)
		lane := Lane{
			ID: j.ID, Name: j.Name, URL: j.URL, Runner: j.Runner, Labels: j.Labels, Conclusion: j.Conclusion,
			Queue: Slice{Name: "waiting for a runner", Kind: "queue", Start: j.CreatedAt, End: j.StartedAt},
			Job:   Slice{Name: j.Name, Kind: "job", Start: j.StartedAt, End: end, Failed: failed(j.Conclusion)},
		}
		for _, s := range j.Steps {
			if s.StartedAt.IsZero() {
				continue
			}
			lane.Job.Children = append(lane.Job.Children, Slice{
				Name: s.Name, Kind: "step", Start: s.StartedAt, End: orNow(s.CompletedAt, run.Now), Failed: failed(s.Conclusion),
			})
		}
		t.Lanes = append(t.Lanes, lane)
	}
	place(t.Lanes, spans)
	slices.SortStableFunc(t.Lanes, func(a, b Lane) int {
		return cmp.Or(a.Queue.Start.Compare(b.Queue.Start), a.Job.Start.Compare(b.Job.Start))
	})
	for i, l := range t.Lanes {
		if i == 0 || l.Queue.Start.Before(t.Start) {
			t.Start = l.Queue.Start
		}
		if l.Job.End.After(t.End) {
			t.End = l.Job.End
		}
	}
	for _, i := range critical(t.Lanes) {
		t.Lanes[i].Critical = true
	}
	return t
}

func failed(conclusion string) bool {
	return conclusion == "failure" || conclusion == "cancelled" || conclusion == "timed_out"
}

func orNow(t, now time.Time) time.Time {
	if t.IsZero() {
		return now
	}
	return t
}

// slack is how much later than a job's end the API may say the next job was queued, and how much
// earlier than a span's start it may say its step started: both are timed to the second.
const slack = time.Second

// held is how long after the jobs before it a job may be queued and still be waiting on them.
const held = 10 * time.Second

// critical is the chain of lanes the run's wall time is made of, first to last: the lane that
// finished last, then the lane whose end let it be queued - the latest to finish by the time it
// was - and so on back to a lane queued when the run began. The jobs API does not say what a job
// needs; a job is queued the moment the last of its needs finishes, so the time says it.
func critical(lanes []Lane) []int {
	if len(lanes) == 0 {
		return nil
	}
	last := 0
	for i, l := range lanes {
		if l.Job.End.After(lanes[last].Job.End) {
			last = i
		}
	}
	chain := []int{last}
	for cur := last; ; {
		var before []int
		for i, l := range lanes {
			if !slices.Contains(chain, i) && !l.Job.End.After(lanes[cur].Queue.Start.Add(slack)) {
				before = append(before, i)
			}
		}
		if len(before) == 0 {
			break
		}
		prev := slices.MaxFunc(before, func(a, b int) int { return lanes[a].Job.End.Compare(lanes[b].Job.End) })
		// A lane that ended long before cur was queued did not hold it up: something outside the
		// run did, a manual approval or an environment's wait.
		if lanes[cur].Queue.Start.Sub(lanes[prev].Job.End) > held {
			break
		}
		chain = append(chain, prev)
		cur = prev
	}
	slices.Reverse(chain)
	return chain
}
