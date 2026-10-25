package proven_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/internal/proven"
)

// trees is a Trees over a table; a sha it has not is an error, as a commit force-pushed away is.
func trees(table map[string]string) proven.Trees {
	return func(_ context.Context, sha string) (string, error) {
		if t, ok := table[sha]; ok {
			return t, nil
		}
		return "", errors.New("no commit " + sha)
	}
}

var events = []string{"schedule", "workflow_dispatch"}

// The answer for a commit is the newest run that finished on its tree, whichever branch it ran on
// and whether it passed: a newer failure on the tree answers over an older pass. A run on the
// commit itself answers without asking for its tree. A cancelled run, a run still going, a run of
// another event - a push - and a run whose head is gone are nobody's answer; with no run left,
// there is none.
func TestTheAnswerIsTheNewestRunThatFinishedOnTheTree(t *testing.T) {
	known := trees(map[string]string{"pr1": "t1", "pr2": "t2", "main1": "t1", "main0": "t0"})
	runs := []proven.Run{
		{ID: 9, HeadSHA: "pr1", Event: "schedule", Status: "completed", Conclusion: "cancelled", URL: "u9"},
		{ID: 8, HeadSHA: "pr1", Event: "schedule", Status: "in_progress", URL: "u8"},
		{ID: 7, HeadSHA: "pr1", Event: "push", Status: "completed", Conclusion: "success", URL: "u7"},
		{ID: 6, HeadSHA: "gone", Event: "workflow_dispatch", Status: "completed", Conclusion: "success", URL: "u6"},
		{ID: 5, HeadSHA: "pr1", Event: "workflow_dispatch", Status: "completed", Conclusion: "failure", URL: "u5"},
		{ID: 4, HeadSHA: "main1", Event: "schedule", Status: "completed", Conclusion: "success", URL: "u4"},
		{ID: 3, HeadSHA: "pr2", Event: "schedule", Status: "completed", Conclusion: "success", URL: "u3"},
	}
	for name, tc := range map[string]struct {
		sha, tree string
		want      int64
		found     bool
	}{
		"by tree, the newest that finished": {sha: "main1", tree: "t1", want: 5, found: true},
		"by the commit itself":              {sha: "pr2", tree: "t2", want: 3, found: true},
		"by the commit before its tree":     {sha: "gone", tree: "t1", want: 6, found: true},
		"no run on the tree":                {sha: "main0", tree: "t0"},
	} {
		t.Run(name, func(t *testing.T) {
			run, found := proven.Answer(t.Context(), runs, tc.sha, tc.tree, events, known)
			assert.Equal(t, tc.found, found)
			assert.Equal(t, tc.want, run.ID)
		})
	}
}

// The base of a run on main is the newest of main's commits that a run which passed ran on, by
// its sha or by its tree, and the run that did. Main's head itself is proven. A pull request's
// head rebased onto an unmoved main merges as the same tree, so its run answers for the merge
// commit - the case that, asked of main's own runs only, left the base where the last run on
// main passed, 332 commits behind. A passed run whose head is gone answers for nothing, and when
// no commit of main has a run, there is no base.
func TestTheBaseIsTheNewestOfMainARunPassedOn(t *testing.T) {
	main := []proven.Commit{{SHA: "m3", Tree: "t3"}, {SHA: "m2", Tree: "t2"}, {SHA: "m1", Tree: "t1"}, {SHA: "m0", Tree: "t0"}}
	known := trees(map[string]string{"pr2": "t2", "pr3": "t3", "m0": "t0", "m3": "t3"})
	for name, tc := range map[string]struct {
		passed []proven.Run
		want   string
		run    int64
		found  bool
	}{
		"main's head passed itself, so it is proven": {
			passed: []proven.Run{{ID: 2, HeadSHA: "m3"}, {ID: 1, HeadSHA: "m0"}}, want: "m3", run: 2, found: true,
		},
		"a pull request's head answers for the merge of its tree": {
			passed: []proven.Run{{ID: 3, HeadSHA: "pr3"}}, want: "m3", run: 3, found: true,
		},
		"spin#659: the run on main is old, the pull request's is newer": {
			passed: []proven.Run{{ID: 5, HeadSHA: "pr2"}, {ID: 4, HeadSHA: "m0"}}, want: "m2", run: 5, found: true,
		},
		"a run on the commit itself answers before one on its tree": {
			passed: []proven.Run{{ID: 7, HeadSHA: "pr2"}, {ID: 6, HeadSHA: "m2"}}, want: "m2", run: 6, found: true,
		},
		"the newest run on the commit answers": {
			passed: []proven.Run{{ID: 7, HeadSHA: "m2"}, {ID: 6, HeadSHA: "m2"}}, want: "m2", run: 7, found: true,
		},
		"a head that is gone answers for nothing": {
			passed: []proven.Run{{ID: 8, HeadSHA: "gone"}, {ID: 1, HeadSHA: "m0"}}, want: "m0", run: 1, found: true,
		},
		"no commit of main ran": {
			passed: []proven.Run{{ID: 9, HeadSHA: "gone"}},
		},
		"no run passed": {},
	} {
		t.Run(name, func(t *testing.T) {
			c, run, found := proven.Base(t.Context(), main, tc.passed, known)
			assert.Equal(t, tc.found, found)
			assert.Equal(t, tc.want, c.SHA)
			assert.Equal(t, tc.run, run.ID)
		})
	}
	// m2 is not among the run heads: the run of its tree is asked for, and found without the API
	// for m3, which is.
	asked := 0
	counting := func(ctx context.Context, sha string) (string, error) { asked++; return known(ctx, sha) }
	_, _, found := proven.Base(t.Context(), main, []proven.Run{{ID: 2, HeadSHA: "m3"}}, counting)
	require.True(t, found)
	assert.Zero(t, asked, "a run on the commit itself needs no tree")
}

// A release is refused unless the gate passed on the commit's tree and the second workflow's
// newest run on the commit passed: each refusal says which and why for the workflow's log, and
// a pass says which run. Without a second workflow, the gate alone answers.
func TestARelease(t *testing.T) {
	passed := proven.Run{Conclusion: "success", Status: "completed", URL: "ci-ok"}
	failed := proven.Run{Conclusion: "failure", Status: "completed", URL: "ci-no"}
	for name, tc := range map[string]struct {
		answer proven.Run
		found  bool
		also   string
		runs   []proven.Run
		want   proven.Verdict
	}{
		"both passed": {
			answer: passed, found: true, also: "kvm.yml",
			runs: []proven.Run{{Status: "completed", Conclusion: "success", URL: "kvm-ok"}},
			want: proven.Verdict{OK: true, Lines: []string{"ci.yml passed on abc's tree: ci-ok", "kvm.yml passed on abc: kvm-ok"}},
		},
		"the gate never ran on the tree": {
			also: "kvm.yml", runs: []proven.Run{{Status: "completed", Conclusion: "success", URL: "kvm-ok"}},
			want: proven.Verdict{Lines: []string{"::error::ci.yml never finished on abc's tree: dispatch it on main (or on the branch it came from)", "kvm.yml passed on abc: kvm-ok"}},
		},
		"the gate failed": {
			answer: failed, found: true, also: "kvm.yml",
			runs: []proven.Run{{Status: "completed", Conclusion: "success", URL: "kvm-ok"}},
			want: proven.Verdict{Lines: []string{"::error::ci.yml did not pass on abc's tree (failure): ci-no", "kvm.yml passed on abc: kvm-ok"}},
		},
		"the second never ran": {
			answer: passed, found: true, also: "kvm.yml",
			want: proven.Verdict{Lines: []string{"ci.yml passed on abc's tree: ci-ok", "::error::kvm.yml never ran on abc from a push to main: a release is made from a commit main tested"}},
		},
		"the second failed": {
			answer: passed, found: true, also: "kvm.yml",
			runs: []proven.Run{{Status: "completed", Conclusion: "failure", URL: "kvm-no"}, {Status: "completed", Conclusion: "success", URL: "kvm-old"}},
			want: proven.Verdict{Lines: []string{"ci.yml passed on abc's tree: ci-ok", "::error::kvm.yml did not pass on abc (failure): kvm-no"}},
		},
		"the second is still running": {
			answer: passed, found: true, also: "kvm.yml",
			runs: []proven.Run{{Status: "in_progress", URL: "kvm-run"}},
			want: proven.Verdict{Lines: []string{"ci.yml passed on abc's tree: ci-ok", "::error::kvm.yml is still running on abc (in_progress): release once it has passed: kvm-run"}},
		},
		"the gate alone": {
			answer: passed, found: true,
			want: proven.Verdict{OK: true, Lines: []string{"ci.yml passed on abc's tree: ci-ok"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, proven.Judge("abc", "ci.yml", tc.answer, tc.found, tc.also, tc.runs))
		})
	}
}

// Each test that ended in a log is a "<seconds> <test>" line, in the log's order, subtests as go
// test prints them; a log with none is none.
func TestEachTestsTimeInALog(t *testing.T) {
	log := "=== RUN   TestA\n--- PASS: TestA (12.50s)\n    --- FAIL: TestA/sub (0.20s)\n--- FAIL: TestB (3s)\nok  \tpkg\t16.0s\n"
	assert.Equal(t, []string{"12.50 TestA", "0.20 TestA/sub", "3 TestB"}, proven.Times(log))
	assert.Empty(t, proven.Times("ok  \tpkg\t16.0s\n"))
}
