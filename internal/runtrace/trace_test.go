package runtrace_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/ghatrace"
	"github.com/spin-stack/go-tools/internal/proven"
	"github.com/spin-stack/go-tools/internal/runtrace"
)

var t0 = time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC)

func at(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }

// job is a completed job queued at q, started at s and finished at e, with steps back to back
// between s and e at the given boundaries.
func job(name, runner string, q, s, e int, steps ...int) proven.Job {
	j := proven.Job{Name: name, Runner: runner, Status: "completed", Conclusion: "success", CreatedAt: at(q), StartedAt: at(s), CompletedAt: at(e)}
	from := s
	for i, to := range append(steps, e) {
		j.Steps = append(j.Steps, proven.Step{Name: "step" + string(rune('A'+i)), StartedAt: at(from), CompletedAt: at(to), Conclusion: "success"})
		from = to
	}
	return j
}

// A plan job, three lanes it unblocks, and a gate that waits for all of them: the run's wall is
// plan, then the slowest lane, then the gate - not the lane that was queued first or ran longest
// on its own.
func runOf() runtrace.Run {
	return runtrace.Run{ID: 7, Workflow: "CI", Now: at(1000), Jobs: []proven.Job{
		job("gate", "r5", 301, 305, 310),
		job("plan", "r1", 0, 2, 40),
		job("lane fast", "r2", 40, 45, 100),
		job("lane slow", "r3", 40, 60, 300, 120),
		job("lane mid", "r4", 40, 41, 250),
		// The API times a skipped job as starting and ending when it was queued.
		{Name: "build", Status: "completed", Conclusion: "skipped", CreatedAt: at(40), StartedAt: at(40), CompletedAt: at(40)},
		{Name: "deploy", Status: "queued", CreatedAt: at(310)},
	}}
}

func names(t runtrace.Trace, critical bool) []string {
	var out []string
	for _, l := range t.Lanes {
		if !critical || l.Critical {
			out = append(out, l.Name)
		}
	}
	return out
}

func TestBuildOrdersLanesAndFindsTheCriticalPath(t *testing.T) {
	tr := runtrace.Build(runOf(), nil)
	assert.Equal(t, []string{"plan", "lane mid", "lane fast", "lane slow", "gate"}, names(tr, false), "by queued, then started")
	assert.Equal(t, []string{"plan", "lane slow", "gate"}, names(tr, true))
	assert.Equal(t, []string{"build", "deploy"}, tr.Skipped)
	assert.Equal(t, 310*time.Second, tr.Wall())
	assert.Equal(t, 20*time.Second, tr.Lanes[3].Queue.Duration(), "lane slow waited from 40 to 60")
}

// A job queued long after the jobs before it ended waited on something outside the run, such as
// an approval, and starts the chain.
func TestCriticalPathStopsAtAWaitNoJobExplains(t *testing.T) {
	for queued, want := range map[int][]string{
		60:  {"build", "deploy"},
		61:  {"deploy"},
		170: {"deploy"},
	} {
		r := runtrace.Run{Jobs: []proven.Job{job("build", "r1", 0, 1, 50), job("deploy", "r2", queued, queued+1, queued+30)}}
		assert.Equal(t, want, names(runtrace.Build(r, nil), true), "deploy queued at %d", queued)
	}
}

func TestAJobThatDidNotSucceedIsFailed(t *testing.T) {
	for conclusion, failed := range map[string]bool{"success": false, "failure": true, "cancelled": true, "timed_out": true} {
		j := job("lane", "r1", 0, 1, 2)
		j.Conclusion = conclusion
		tr := runtrace.Build(runtrace.Run{Jobs: []proven.Job{j}}, nil)
		assert.Equal(t, failed, tr.Lanes[0].Job.Failed, conclusion)
	}
}

func TestAJobStillRunningEndsNow(t *testing.T) {
	j := job("trace", "r1", 0, 1, 2)
	j.CompletedAt, j.Status, j.Conclusion = time.Time{}, "in_progress", ""
	j.Steps[0].CompletedAt = time.Time{}
	j.Steps = append(j.Steps, proven.Step{Name: "pending"})
	tr := runtrace.Build(runtrace.Run{Now: at(9), Jobs: []proven.Job{j}}, nil)
	require.Len(t, tr.Lanes, 1)
	assert.Equal(t, at(9), tr.Lanes[0].Job.End)
	require.Len(t, tr.Lanes[0].Job.Children, 1, "a step that has not started is not drawn")
	assert.Equal(t, at(9), tr.Lanes[0].Job.Children[0].End)
}

func span(name, runner string, s, e float64, exit int) ghatrace.Span {
	d := func(x float64) time.Time { return t0.Add(time.Duration(x * float64(time.Second))) }
	return ghatrace.Span{Name: name, Runner: runner, Start: d(s), End: d(e), Exit: exit}
}

func TestSpansGoUnderTheStepTheyRanInAndNest(t *testing.T) {
	spans := []ghatrace.Span{
		span("test A", "r3", 125, 295, 0),
		span("up", "r3", 60.4, 110, 0),
		// Inside step B by its real start, though B's floored start and A's floored end are both 120.
		span("test B", "r3", 120.3, 290, 1),
		span("build", "r3", 61, 90, 0),
		// r3 ran no job at 500: a span of some other run's.
		span("stray", "r3", 500, 501, 0),
		// r9 ran no job of this run.
		span("other", "r9", 61, 62, 0),
		// Ends past A's floored end of 120, as a span that ran to A's real end does.
		span("tail", "r3", 119.5, 120.6, 0),
	}
	tr := runtrace.Build(runOf(), spans)
	slow := tr.Lanes[3]
	require.Equal(t, "lane slow", slow.Name)
	assert.Len(t, slow.Job.Children, 2, "every span is under a step, none beside them")
	stepA, stepB := slow.Job.Children[0], slow.Job.Children[1]
	require.Len(t, stepA.Children, 2)
	assert.Equal(t, "up", stepA.Children[0].Name)
	assert.Equal(t, "tail", stepA.Children[1].Name, "under the step it started in")
	require.Len(t, stepA.Children[0].Children, 1)
	assert.Equal(t, "build", stepA.Children[0].Children[0].Name, "nested inside the span that contains it")
	require.Len(t, stepB.Children, 2)
	assert.Equal(t, "test B", stepB.Children[0].Name)
	assert.True(t, stepB.Children[0].Failed)
	assert.Equal(t, "test A", stepB.Children[1].Name, "overlaps test B without being inside it: a sibling")
	for _, l := range tr.Lanes {
		if l.Name != "lane slow" {
			for _, s := range l.Job.Children {
				assert.Empty(t, s.Children, l.Name)
			}
		}
	}
}

// Two lanes side by side in one step: by the time alone, every part of lint is inside test too.
func TestASpanGoesUnderTheParentItNames(t *testing.T) {
	part := func(name, parent string, s, e float64) ghatrace.Span {
		sp := span(name, "r3", s, e, 0)
		sp.Parent = parent
		return sp
	}
	spans := []ghatrace.Span{
		span("lint", "r3", 130, 290, 0),
		span("test", "r3", 135, 280, 0),
		part("golangci-lint", "lint", 140, 200),
		part("deadcode", "lint", 200, 210),
		part("go test", "test", 140, 270),
		part("race", "go test", 150, 260),
		// Named, but no span of that name holds it: time decides, and only test holds it.
		part("stray", "nothing", 271, 275),
	}
	tr := runtrace.Build(runOf(), spans)
	stepB := tr.Lanes[3].Job.Children[1]
	tree := map[string][]string{}
	var walk func(s runtrace.Slice)
	walk = func(s runtrace.Slice) {
		for _, c := range s.Children {
			tree[s.Name] = append(tree[s.Name], c.Name)
			walk(c)
		}
	}
	walk(stepB)
	assert.Equal(t, map[string][]string{
		"stepB":   {"lint"},
		"lint":    {"test", "golangci-lint", "deadcode"},
		"test":    {"go test", "stray"},
		"go test": {"race"},
	}, tree)
}

// track is one of a process's tracks in a Perfetto trace.
type track struct{ pid, tid int }

// perfetto is a trace as WritePerfetto wrote it, read back: the processes' names, the names of
// the spans and the waits, and each track's slices and names.
type perfetto struct {
	processes, spanNames, waits []string
	tracks                      map[track][][2]int64
	trackNames                  map[track][]string
}

func perfettoOf(t *testing.T, tr runtrace.Trace) perfetto {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, runtrace.WritePerfetto(&buf, tr))
	var doc struct {
		Events []struct {
			Ph   string         `json:"ph"`
			Name string         `json:"name"`
			PID  int            `json:"pid"`
			TID  int            `json:"tid"`
			TS   int64          `json:"ts"`
			Dur  int64          `json:"dur"`
			Args map[string]any `json:"args"`
		} `json:"traceEvents"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))
	p := perfetto{tracks: map[track][][2]int64{}, trackNames: map[track][]string{}}
	for _, e := range doc.Events {
		k := track{e.PID, e.TID}
		switch {
		case e.Ph == "M" && e.Name == "process_name":
			p.processes = append(p.processes, fmt.Sprint(e.Args["name"]))
		case e.Ph == "M" && e.Name == "thread_name":
			p.trackNames[k] = append(p.trackNames[k], fmt.Sprint(e.Args["name"]))
		case e.Ph == "X" && strings.HasPrefix(e.Name, "waiting for a runner"):
			p.waits = append(p.waits, e.Name)
			p.tracks[k] = append(p.tracks[k], [2]int64{e.TS, e.TS + e.Dur})
		case e.Ph == "X":
			p.tracks[k] = append(p.tracks[k], [2]int64{e.TS, e.TS + e.Dur})
			if e.TID > 1 {
				p.spanNames = append(p.spanNames, e.Name)
			}
		}
	}
	return p
}

// Perfetto draws a track right only when every two slices on it nest or are apart.
func TestPerfettoTracksHoldOnlyNestedOrDisjointSlices(t *testing.T) {
	spans := []ghatrace.Span{
		span("up", "r3", 60.4, 110, 0), span("build", "r3", 61, 90, 0),
		// push starts once build has ended, inside up, and ends after up: not on up's track.
		span("push", "r3", 95, 115, 0),
		// test A starts inside test B and ends after it: they cannot share a track.
		span("test A", "r3", 125, 295, 0), span("test B", "r3", 120.3, 290, 1),
		span("test C", "r3", 130, 140, 0),
	}
	r := runOf()
	r.Jobs[1].CreatedAt = r.Jobs[1].StartedAt // plan did not wait
	p := perfettoOf(t, runtrace.Build(r, spans))
	processes, spanNames, waits, tracks, trackNames := p.processes, p.spanNames, p.waits, p.tracks, p.trackNames
	assert.Equal(t, []string{"plan (critical path)", "lane mid", "lane fast", "lane slow (critical path)", "gate (critical path)"}, processes)
	assert.ElementsMatch(t, []string{"up 50s", "build 29s", "push 20s", "✗ test B 2m50s", "test A 2m50s", "test C 10s"}, spanNames)
	assert.Equal(t, []string{"waiting for a runner 1s", "waiting for a runner 5s", "waiting for a runner 20s", "waiting for a runner 4s"}, waits, "no bar for plan, which did not wait")
	var slowTracks int
	for k := range tracks {
		want := "spans"
		if k.tid == 1 {
			want = "steps"
		}
		assert.Equal(t, []string{want}, trackNames[k], "track %v", k)
		if k.pid == 4 && k.tid > 1 {
			slowTracks++
		}
	}
	assert.Equal(t, 2, slowTracks, "lane slow's spans fit on two tracks: up, build, test B, test C; push, test A")
	for k, slices := range tracks {
		for i, a := range slices {
			for _, b := range slices[i+1:] {
				nested := (a[0] <= b[0] && b[1] <= a[1]) || (b[0] <= a[0] && a[1] <= b[1])
				apart := a[1] <= b[0] || b[1] <= a[0]
				assert.True(t, nested || apart, "track %v: %v and %v overlap", k, a, b)
			}
		}
	}
}

func TestSummary(t *testing.T) {
	r := runOf()
	r.Jobs[2].Name = "lane: fast #1; x|y"
	var buf bytes.Buffer
	require.NoError(t, runtrace.WriteSummary(&buf, runtrace.Build(r, nil), "see the artifacts"))
	out := buf.String()
	assert.Contains(t, out, "| **5m10s** | 9m07s (11 job-minutes rounded up) | 32s | 20s (lane slow) |")
	assert.Contains(t, out, "**Critical path**: plan (38s, waited 2s) → lane slow (4m00s, waited 20s) → gate (5s, waited 4s)")
	assert.Contains(t, out, "|---|---|---|\n| stepA | lane mid | 3m29s |\n| stepB | lane slow | 3m00s |\n", "the slowest first")
	assert.Contains(t, out, "| stepA | lane: fast #1; x\\|y | 55s |\n", "a pipe in a name does not end its cell")
	assert.Contains(t, out, "Did not run: build, deploy.")
	assert.True(t, strings.HasSuffix(out, "see the artifacts\n"))
}

func TestSummaryOfOneJobAndOfNone(t *testing.T) {
	// Queued as it started, and done in the same second.
	one := runtrace.Run{Workflow: "Lint", Jobs: []proven.Job{job("lint", "r1", 5, 5, 5), {Name: "fix", Conclusion: "skipped"}}}
	var buf bytes.Buffer
	require.NoError(t, runtrace.WriteSummary(&buf, runtrace.Build(one, nil), ""))
	out := buf.String()
	assert.Contains(t, out, "**Critical path**: lint (0s, waited 0s)\n\n| slowest | job | took |\n")
	assert.Contains(t, out, "| stepA | lint | 0s |")
	assert.Contains(t, out, "Did not run: fix.")

	buf.Reset()
	require.NoError(t, runtrace.WriteSummary(&buf, runtrace.Build(runtrace.Run{Workflow: "Empty"}, nil), ""))
	assert.Equal(t, "## Empty: where the time went\n\n| wall | on runners | waiting for runners | longest wait |\n|---|---|---|---|\n| **0s** | 0s (0 job-minutes rounded up) | 0s | - |\n\n", buf.String())
}

func TestHTMLPageEscapesWhatAJobIsCalled(t *testing.T) {
	r := runOf()
	r.Jobs[1].Name = "</script><script>alert(1)</script>"
	var buf bytes.Buffer
	require.NoError(t, runtrace.WriteHTML(&buf, runtrace.Build(r, nil)))
	page := buf.String()
	assert.Equal(t, 1, strings.Count(page, "<script>"), "only the page's own script")
	assert.Contains(t, page, `"wall":310000`)
	assert.Contains(t, page, `"n":"stepB"`, "a job's steps are on the page")
}

// stamp is a log line's time, s seconds into the run, as GitHub writes it.
func stamp(s float64) string {
	return t0.Add(time.Duration(s * float64(time.Second))).Format("2006-01-02T15:04:05.0000000Z")
}

// A step's own steps are in the job's log, between the runner's start-action and end-action
// lines: they go under the step that ran them - a composite action's inside the one that ran it -
// failed when they failed, and the summary lists them rather than the step that holds them.
func TestACompositeActionsStepsComeFromTheLog(t *testing.T) {
	j := job("lane", "r1", 0, 10, 120, 30)
	j.ID = 7
	j.Steps[0].Name, j.Steps[1].Name = "Run ./.github/actions/fleet-setup", "The lane"
	log := strings.Join([]string{
		"\ufeff" + stamp(9.5) + " Requested labels: kvm",
		stamp(10.12) + " ##[group]Run ./.github/actions/fleet-setup",
		stamp(10.14) + " ##[start-action display=Set up Node.js;id=__self.__actions_setup-node]",
		stamp(10.15) + " with: node-version 20",
		stamp(13.33) + " ##[end-action id=__self.__actions_setup-node;outcome=success;conclusion=success;duration_ms=3190]",
		stamp(13.34) + " ##[start-action display=Toolchains; Go and Node;id=__self.__tools]",
		stamp(13.40) + " ##[start-action display=Set up Go;id=__tools.__setup-go]",
		stamp(18.10) + " ##[end-action id=__tools.__setup-go;outcome=success;conclusion=success;duration_ms=4700]",
		stamp(18.15) + " ##[end-action id=__self.__tools;outcome=success;conclusion=success;duration_ms=4810]",
		"not a line of the log",
		stamp(18.2) + " ##[start-action display=AppArmor;id=__self.__run]",
		stamp(29.9) + " ##[end-action id=__self.__run;outcome=failure;conclusion=failure;duration_ms=11700]",
		stamp(30.4) + " ##[group]Run go tool task ci:lane",
		// Started and never ended: the job was cancelled under it.
		stamp(100) + " ##[start-action display=Never ends;id=__self.__never]",
		stamp(101) + " ##[end-action id=__self.__unknown;outcome=success;conclusion=success;duration_ms=1]",
	}, "\n")
	tr := runtrace.Build(runtrace.Run{Jobs: []proven.Job{j}, Logs: map[int64]string{7: log}}, nil)
	setup, lane := tr.Lanes[0].Job.Children[0], tr.Lanes[0].Job.Children[1]
	tree := map[string][]string{}
	var walk func(s runtrace.Slice)
	walk = func(s runtrace.Slice) {
		for _, c := range s.Children {
			tree[s.Name] = append(tree[s.Name], fmt.Sprintf("%s %s failed=%t", c.Name, c.Duration(), c.Failed))
			walk(c)
		}
	}
	walk(setup)
	assert.Equal(t, map[string][]string{
		"Run ./.github/actions/fleet-setup": {"Set up Node.js 3.19s failed=false", "Toolchains; Go and Node 4.81s failed=false", "AppArmor 11.7s failed=true"},
		"Toolchains; Go and Node":           {"Set up Go 4.7s failed=false"},
	}, tree)
	assert.Empty(t, lane.Children, "an action that never ended, or ended without starting, is no step")

	var buf bytes.Buffer
	require.NoError(t, runtrace.WriteSummary(&buf, tr, ""))
	assert.Contains(t, buf.String(), "| AppArmor | lane | 12s |")
	assert.NotContains(t, buf.String(), "| Run ./.github/actions/fleet-setup |", "the step holds its parts: they are what is listed")
	assert.NotContains(t, buf.String(), "| Toolchains; Go and Node |", "nor one that holds a single part")
}

// The page the site shows is this package's own, drawn from a real run (go-tools' gate, run
// 42672797946, testdata/example.json, its steps named without their action's commit, which only
// versions.yaml may write): a test writes it, so it is never older than the code. UPDATE_GOLDEN=1
// writes it.
func TestTheExampleTheSiteShows(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "example.json"))
	require.NoError(t, err)
	var tr runtrace.Trace
	require.NoError(t, json.Unmarshal(raw, &tr))
	var buf bytes.Buffer
	require.NoError(t, runtrace.WriteHTML(&buf, tr))
	path := filepath.Join("..", "..", "site", "public", "ghatrace", "example.html")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), buf.String())
}
