package ghatrace

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
}

type pageLane struct {
	URL      string   `json:"url"`
	Runner   string   `json:"runner"`
	Labels   []string `json:"labels"`
	Critical bool     `json:"critical"`
	Queue    node     `json:"queue"`
	Job      node     `json:"job"`
}

// WriteHTML writes t as one page with nothing to fetch: the jobs as a waterfall, each opening to
// its steps and their spans.
func WriteHTML(w io.Writer, t Trace) error {
	ms := func(at time.Time) int64 { return at.Sub(t.Start).Milliseconds() }
	var conv func(s Slice) node
	conv = func(s Slice) node {
		n := node{N: s.Name, K: s.Kind, S: ms(s.Start), E: ms(s.End), F: s.Failed}
		for _, c := range s.Children {
			n.C = append(n.C, conv(c))
		}
		return n
	}
	lanes := []pageLane{}
	for _, l := range t.Lanes {
		lanes = append(lanes, pageLane{URL: l.URL, Runner: l.Runner, Labels: l.Labels, Critical: l.Critical, Queue: conv(l.Queue), Job: conv(l.Job)})
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
