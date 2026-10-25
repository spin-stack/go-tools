package proven

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// failLine is a top-level test's failure as go test prints it, its time in parentheses after the
// name: a subtest's name has a slash in it and is its parent's business. Whatever a CI log puts
// before it on the line - a job's name, a timestamp - is let through.
var failLine = regexp.MustCompile(`--- FAIL: (Test[A-Za-z0-9_]+) \(`)

// Failed is every top-level test a log says failed, each once, in order.
func Failed(log string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range failLine.FindAllStringSubmatch(log, -1) { // mutate-exempt: any n < 0 is every match
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// FailedIn is every top-level test the text files of a zip archive say failed - a lane's logs,
// kept as a run's artifact when it fails.
func FailedIn(archive []byte) ([]string, error) {
	r, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("the artifact is not a zip: %w", err)
	}
	var out []string
	seen := map[string]bool{}
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		text, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		for _, t := range Failed(string(text)) {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out, nil
}

// Kind is what a test's failures across runs say about it.
type Kind string

const (
	// Flaky is a test that did not fail in another finished run on a tree it failed on: the same
	// bytes, another answer. A rerun of the commit is what shows it.
	Flaky Kind = "flaky"
	// Intermittent is a test that, on one branch, failed, did not fail on a later or earlier finished
	// run, and failed again: on and off across commits, which a change in between could explain and
	// a flake explains better.
	Intermittent Kind = "intermittent"
	// Red is a test that failed on finished runs in a row on a branch, nothing in between where it
	// did not: the code was broken then.
	Red Kind = "red"
	// Once is a test that failed and has no other finished run on its tree or beside it on its
	// branch to be judged by: a rerun of the commit would tell.
	Once Kind = "once"
)

// Failure is one test's failures across a workflow's runs, and what they say about it.
type Failure struct {
	Test     string
	Runs     []Run           // the runs it failed in, newest first
	Branches []string        // sorted, each once
	Trees    map[string]bool // the trees it failed on
	Kind     Kind
}

// Flakes judges every failed test of runs (newest first, one workflow) by failed, each run's
// failed tests. A cancelled run finished nothing and says nothing; a run whose failed tests are
// unknown (its logs gone) counts as a run the test did not fail in only when it passed.
func Flakes(ctx context.Context, runs []Run, failed map[int64][]string, trees Trees) ([]Failure, error) {
	tree := map[int64]string{}
	var finished []Run
	for _, r := range runs {
		if !r.Finished() {
			continue
		}
		t, err := trees(ctx, r.HeadSHA)
		if err != nil {
			return nil, fmt.Errorf("run %d's tree: %w", r.ID, err)
		}
		tree[r.ID] = t
		finished = append(finished, r)
	}
	byTest := map[string]*Failure{}
	for _, r := range finished {
		for _, test := range failed[r.ID] {
			f := byTest[test]
			if f == nil {
				f = &Failure{Test: test, Trees: map[string]bool{}}
				byTest[test] = f
			}
			f.Runs = append(f.Runs, r)
			f.Trees[tree[r.ID]] = true
			if !slices.Contains(f.Branches, r.HeadBranch) {
				f.Branches = append(f.Branches, r.HeadBranch)
			}
		}
	}
	var out []Failure
	for _, f := range byTest {
		f.Kind = kind(*f, finished, failed, tree)
		sort.Strings(f.Branches)
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Runs) != len(out[j].Runs) {
			return len(out[i].Runs) > len(out[j].Runs)
		}
		return out[i].Test < out[j].Test
	})
	return out, nil
}

// outcome is what a finished run says of one test: it failed, it did not (the run passed, or
// failed on other tests alone), or nothing (the run failed and its logs are gone).
func outcome(r Run, test string, failed map[int64][]string) (failedIt, known bool) {
	if slices.Contains(failed[r.ID], test) {
		return true, true
	}
	return false, r.Passed() || len(failed[r.ID]) > 0
}

// kind reads the test's outcomes: on its trees (Flaky), then along each branch's finished runs
// in order (Intermittent, Red), else Once.
func kind(f Failure, finished []Run, failed map[int64][]string, tree map[int64]string) Kind {
	for _, r := range finished {
		if failedIt, known := outcome(r, f.Test, failed); f.Trees[tree[r.ID]] && known && !failedIt {
			return Flaky
		}
	}
	red := false
	for _, branch := range f.Branches {
		// The branch's finished runs with a known outcome, newest first.
		var outcomes []bool
		for _, r := range finished {
			if r.HeadBranch != branch {
				continue
			}
			if failedIt, known := outcome(r, f.Test, failed); known {
				outcomes = append(outcomes, failedIt)
			}
		}
		for i := 1; i < len(outcomes); i++ {
			if outcomes[i] && outcomes[i-1] {
				red = true
			}
		}
		// Failed, did not, failed: on and off. It failed on this branch, so a first and a last are
		// there to find.
		first, last := slices.Index(outcomes, true), len(outcomes)-1-slices.Index(reverse(outcomes), true)
		if last > first && slices.Contains(outcomes[first:last], false) {
			return Intermittent
		}
	}
	if red {
		return Red
	}
	return Once
}

func reverse(b []bool) []bool {
	out := make([]bool, len(b))
	for i, v := range b {
		out[len(b)-1-i] = v
	}
	return out
}

// Report is the failures as a table for a run's summary and a person's eye: the flaky ones first
// as they are the ones a rerun answers, then the red ones, which were the code.
func Report(failures []Failure, runs int) []string {
	lines := []string{"| test | failed | of runs | branches | verdict |", "| --- | --- | --- | --- | --- |"}
	said := map[Kind]string{
		Flaky:        "flaky: did not fail on another run of the same tree",
		Intermittent: "intermittent: on and off along its branch",
		Red:          "red: failed on runs in a row",
		Once:         "once: no other run of its tree to tell",
	}
	for _, f := range failures {
		verdict := said[f.Kind]
		ids := make([]string, 0, len(f.Runs))
		for _, r := range f.Runs {
			ids = append(ids, fmt.Sprintf("[%d](%s)", r.ID, r.URL))
		}
		lines = append(lines, fmt.Sprintf("| %s | %d (%s) | %d | %s | %s |", f.Test, len(f.Runs), strings.Join(ids, " "), runs, strings.Join(f.Branches, ", "), verdict))
	}
	return lines
}
