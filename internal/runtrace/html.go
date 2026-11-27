package runtrace

import (
	_ "embed"
	"html/template"
	"io"
	"strings"
	"time"
)

//go:embed page.html
var page string

var pageTemplate = template.Must(template.New("page").Parse(page))

// node is a slice as the page draws it: milliseconds from the run's start, short keys, because a
// run with a few thousand spans is a few thousand of these in the page.
type node struct {
	N string `json:"n"`
	K string `json:"k"`
	S int64  `json:"s"`
	E int64  `json:"e"`
	F bool   `json:"f,omitempty"`
	C []node `json:"c,omitempty"`
	// T is what its command used and what its machine did meanwhile, a line each, for its tooltip.
	T string `json:"t,omitempty"`
}

type pageLane struct {
	URL      string   `json:"url"`
	Runner   string   `json:"runner"`
	Labels   []string `json:"labels"`
	Critical bool     `json:"critical"`
	Queue    node     `json:"queue"`
	Job      node     `json:"job"`
	// Load is the machine's load as the page draws it under the job: from, to, the share of all
	// CPUs busy, and the percentage of the time tasks waited on I/O - stalled on it, or the CPUs'
	// iowait where the machine has no pressure to say it.
	Load [][4]float64 `json:"load,omitempty"`
}

// WriteHTML writes t as one page with nothing to fetch: the jobs as a waterfall, each opening to
// its steps and their spans.
func WriteHTML(w io.Writer, t Trace) error {
	ms := func(at time.Time) int64 { return at.Sub(t.Start).Milliseconds() }
	lanes := []pageLane{}
	for _, l := range t.Lanes {
		var conv func(s Slice) node
		conv = func(s Slice) node {
			n := node{N: s.Name, K: s.Kind, S: ms(s.Start), E: ms(s.End), F: s.Failed}
			if s.Kind != "queue" {
				n.T = tip(usageText(s.Usage), loadText(over(l.Load, s.Start, s.End)))
			}
			for _, c := range s.Children {
				n.C = append(n.C, conv(c))
			}
			return n
		}
		pl := pageLane{URL: l.URL, Runner: l.Runner, Labels: l.Labels, Critical: l.Critical, Queue: conv(l.Queue), Job: conv(l.Job)}
		for _, ld := range l.Load {
			io, ok := ld.Of[ioStall]
			if !ok {
				io = ld.Of[iowait]
			}
			share := 0.0
			if n := ld.Of[cpus]; n > 0 {
				share = ld.Of[busy] / n
			}
			pl.Load = append(pl.Load, [4]float64{float64(ms(ld.Start)), float64(ms(ld.End)), share, io})
		}
		lanes = append(lanes, pl)
	}
	st := measure(t)
	return pageTemplate.Execute(w, map[string]any{
		"Title": t.Title, "Run": t.Run, "URL": t.URL,
		"Wall": short(st.Wall), "Runner": short(st.Runner), "Minutes": st.Minutes,
		"Queued": short(st.Queued), "Worst": worst(st),
		"Skipped": strings.Join(t.Skipped, ", "),
		// html/template writes a value in a script as JSON, escaped for where it stands.
		"Data": map[string]any{"wall": st.Wall.Milliseconds(), "lanes": lanes},
	})
}

// tip is a slice's tooltip: what its command used, and what the machine did meanwhile.
func tip(usage, machine string) string {
	var lines []string
	if usage != "" {
		lines = append(lines, "its command: "+usage)
	}
	if machine != "" {
		lines = append(lines, "machine: "+machine)
	}
	return strings.Join(lines, "\n")
}
