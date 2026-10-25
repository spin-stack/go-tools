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

// Failure is one test's failures across a workflow's runs, and what they say about it.
type Failure struct {
	Test     string
	Runs     []Run           // the runs it failed in, newest first
	Branches []string        // sorted, each once
	Trees    map[string]bool // the trees it failed on
	// Flaky is whether some tree it failed on has another finished run of the same workflow in
	// which it did not fail: the same bytes, another answer. A test that failed on every run of
	// every tree it failed on is red, not flaky - the code was broken then.
	Flaky bool
}

// Flakes judges every failed test of runs (newest first, one workflow) by failed, each run's
// failed tests: which are flaky and which were red. A cancelled run finished nothing and says
// nothing; a run whose failed tests are unknown (no logs) counts as a run the test did not fail in
// only when it passed.
func Flakes(ctx context.Context, runs []Run, failed map[int64][]string, trees Trees) ([]Failure, error) {
	tree := map[int64]string{}
	for _, r := range runs {
		if !r.Finished() {
			continue
		}
		t, err := trees(ctx, r.HeadSHA)
		if err != nil {
			return nil, fmt.Errorf("run %d's tree: %w", r.ID, err)
		}
		tree[r.ID] = t
	}
	byTest := map[string]*Failure{}
	for _, r := range runs {
		if _, ok := tree[r.ID]; !ok {
			continue
		}
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
		for _, r := range runs {
			t, ok := tree[r.ID]
			if !ok || !f.Trees[t] {
				continue
			}
			// Another run on a tree it failed on, where it did not fail: passed, or failed on others.
			if (r.Passed() || len(failed[r.ID]) > 0) && !slices.Contains(failed[r.ID], f.Test) {
				f.Flaky = true
				break
			}
		}
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

// Report is the failures as a table for a run's summary and a person's eye: the flaky ones first
// as they are the ones a rerun answers, then the red ones, which were the code.
func Report(failures []Failure, runs int) []string {
	lines := []string{"| test | failed | of runs | branches | verdict |", "| --- | --- | --- | --- | --- |"}
	for _, f := range failures {
		verdict := "red: failed on every run of its trees"
		if f.Flaky {
			verdict = "flaky: passed on the same tree"
		}
		ids := make([]string, 0, len(f.Runs))
		for _, r := range f.Runs {
			ids = append(ids, fmt.Sprintf("[%d](%s)", r.ID, r.URL))
		}
		lines = append(lines, fmt.Sprintf("| %s | %d (%s) | %d | %s | %s |", f.Test, len(f.Runs), strings.Join(ids, " "), runs, strings.Join(f.Branches, ", "), verdict))
	}
	return lines
}
