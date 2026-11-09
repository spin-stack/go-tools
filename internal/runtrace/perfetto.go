package runtrace

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

// event is one Chrome trace event, the JSON https://ui.perfetto.dev opens.
type event struct {
	Ph   string         `json:"ph"`
	PID  int            `json:"pid"`
	TID  int            `json:"tid"`
	Name string         `json:"name"`
	TS   int64          `json:"ts"`
	Dur  int64          `json:"dur,omitempty"`
	Args map[string]any `json:"args,omitempty"`
}

// WritePerfetto writes t as Chrome trace events: a process per job, in the order they were queued,
// with a track for the wait and the job's steps and as many tracks for its spans as it takes for
// each to hold only spans that nest or follow one another - Perfetto draws nothing else right.
func WritePerfetto(w io.Writer, t Trace) error {
	us := func(at time.Time) int64 { return at.Sub(t.Start).Microseconds() }
	var events []event
	for i, l := range t.Lanes {
		pid := i + 1 // mutate-exempt: any number a process has to itself, but 0, which Perfetto takes for the idle process
		name := l.Name
		if l.Critical {
			name += " (critical path)"
		}
		events = append(events,
			event{Ph: "M", PID: pid, Name: "process_name", Args: map[string]any{"name": name}},
			event{Ph: "M", PID: pid, Name: "process_sort_index", Args: map[string]any{"sort_index": i}},
			event{Ph: "M", PID: pid, TID: 1, Name: "thread_name", Args: map[string]any{"name": "steps"}},
		)
		add := func(tid int, s Slice, args map[string]any) {
			events = append(events, event{Ph: "X", PID: pid, TID: tid, Name: label(s), TS: us(s.Start), Dur: s.End.Sub(s.Start).Microseconds(), Args: args})
		}
		if l.Queue.End.After(l.Queue.Start) {
			add(1, l.Queue, nil)
		}
		add(1, l.Job, map[string]any{"conclusion": l.Conclusion, "runner": l.Runner, "labels": strings.Join(l.Labels, ","), "url": l.URL})
		var spans []Slice
		for _, s := range l.Job.Children {
			if s.Kind == "step" {
				add(1, s, nil)
				spans = append(spans, flatten(s.Children)...)
			} else {
				spans = append(spans, flatten([]Slice{s})...)
			}
		}
		for j, track := range pack(spans) {
			tid := j + 2 // mutate-exempt: any number that is not the steps' track
			events = append(events, event{Ph: "M", PID: pid, TID: tid, Name: "thread_name", Args: map[string]any{"name": "spans"}})
			for _, s := range track {
				add(tid, s, nil)
			}
		}
	}
	return json.NewEncoder(w).Encode(map[string]any{"traceEvents": events, "displayTimeUnit": "ms"})
}

// label is a bar's name with its length, and a cross when what it stands for did not succeed.
func label(s Slice) string {
	name := s.Name + " " + short(s.Duration())
	if s.Failed {
		name = "✗ " + name
	}
	return name
}

func flatten(in []Slice) []Slice {
	var out []Slice
	for _, s := range in {
		out = append(out, s)
		out = append(out, flatten(s.Children)...)
	}
	return out
}

// pack spreads spans over tracks, by start and the longer first, each on the first track where it
// nests inside the innermost open span or starts after it closed.
func pack(spans []Slice) [][]Slice {
	slices.SortStableFunc(spans, func(a, b Slice) int { return outerFirst(a.Start, a.End, b.Start, b.End) })
	var tracks [][]Slice
	var open [][]time.Time
	for _, s := range spans {
		placed := false
		for i := range tracks {
			for len(open[i]) > 0 && !open[i][len(open[i])-1].After(s.Start) {
				open[i] = open[i][:len(open[i])-1]
			}
			if len(open[i]) == 0 || !s.End.After(open[i][len(open[i])-1]) {
				tracks[i] = append(tracks[i], s)
				open[i] = append(open[i], s.End)
				placed = true
				break
			}
		}
		if !placed {
			tracks = append(tracks, []Slice{s})
			open = append(open, []time.Time{s.End})
		}
	}
	return tracks
}

// short is a duration as a person reads it in a bar: 7s, 2m05s, 1h03m.
func short(d time.Duration) string {
	s := int64(d.Round(time.Second) / time.Second)
	switch {
	case s >= 3600:
		return fmt.Sprintf("%dh%02dm", s/3600, s%3600/60)
	case s >= 60:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	case d > 0 && d < time.Second: // mutate-exempt: d > 1 differs at one nanosecond alone
		return fmt.Sprintf("%dms", d.Milliseconds())
	default:
		return fmt.Sprintf("%ds", s)
	}
}
