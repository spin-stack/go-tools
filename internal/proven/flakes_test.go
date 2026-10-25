package proven_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/internal/proven"
)

// A failed test is a top-level `--- FAIL:` line, each once, with or without a CI timestamp before
// it; a subtest's failure is its parent's.
func TestFailedIsEveryTopLevelFailLine(t *testing.T) {
	log := "=== RUN   TestA\n--- FAIL: TestA (0.10s)\n    --- FAIL: TestA/sub (0.01s)\nshort\tThe lane\t2026-10-25T12:00:00.0Z --- FAIL: TestB (2s)\n--- PASS: TestC (1s)\n--- FAIL: TestA (0.2s)\nx --- FAIL: TestD\n"
	assert.Equal(t, []string{"TestA", "TestB"}, proven.Failed(log))
	assert.Empty(t, proven.Failed(""))
}

func archive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := w.Create(name)
		require.NoError(t, err)
		_, err = f.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func TestFailedInReadsEveryFileOfTheArtifact(t *testing.T) {
	got, err := proven.FailedIn(archive(t, map[string]string{
		"_output/ci-local/integration.log": "--- FAIL: TestB (1s)\n",
		"_output/ci-local/failed":          "integration\n",
		"dir/":                             "",
		"_output/ci-local/short.log":       "--- FAIL: TestA (1s)\n--- FAIL: TestB (1s)\n",
	}))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"TestA", "TestB"}, got)
	_, err = proven.FailedIn([]byte("not a zip"))
	assert.ErrorContains(t, err, "is not a zip")
}

// A test is flaky when another finished run on a tree it failed on did not fail it - passed, or
// failed on other tests; intermittent when along one branch it failed, did not, and failed again;
// red when it failed on finished runs in a row; once when nothing else tells. A cancelled run and
// a run in progress say nothing either way, and so does a failed run whose logs are gone.
func TestAFailedTestIsJudgedByItsTreesThenAlongItsBranch(t *testing.T) {
	runs := []proven.Run{
		{ID: 14, HeadSHA: "c14", HeadBranch: "solo", Status: "completed", Conclusion: "failure"}, // TestG, then a pass on another tree
		{ID: 13, HeadSHA: "c13", HeadBranch: "solo", Status: "completed", Conclusion: "success"},
		{ID: 12, HeadSHA: "c12", HeadBranch: "dev", Status: "completed", Conclusion: "failure"}, // TestF
		{ID: 11, HeadSHA: "c11", HeadBranch: "dev", Status: "completed", Conclusion: "success"},
		{ID: 10, HeadSHA: "c10", HeadBranch: "dev", Status: "completed", Conclusion: "failure"}, // TestF: on and off
		{ID: 9, HeadSHA: "c9", HeadBranch: "pr", Status: "in_progress"},
		{ID: 8, HeadSHA: "c8", HeadBranch: "pr", Status: "completed", Conclusion: "cancelled"},
		{ID: 7, HeadSHA: "c7", HeadBranch: "pr", Status: "completed", Conclusion: "success"},   // same tree as 6
		{ID: 6, HeadSHA: "c6", HeadBranch: "pr", Status: "completed", Conclusion: "failure"},   // TestA
		{ID: 5, HeadSHA: "c5", HeadBranch: "main", Status: "completed", Conclusion: "failure"}, // TestB, TestC
		{ID: 4, HeadSHA: "c4", HeadBranch: "main", Status: "completed", Conclusion: "failure"}, // TestC; same tree as 5
		{ID: 3, HeadSHA: "c3", HeadBranch: "old", Status: "completed", Conclusion: "failure"},  // TestD
		{ID: 2, HeadSHA: "c2", HeadBranch: "old", Status: "completed", Conclusion: "failure"},  // same tree as 3; its logs gone
	}
	trees := func(_ context.Context, sha string) (string, error) {
		return map[string]string{"c14": "t14", "c13": "t13", "c12": "t12", "c11": "t11", "c10": "t10", "c9": "t9", "c8": "t8", "c7": "t67", "c6": "t67", "c5": "t45", "c4": "t45", "c3": "t3", "c2": "t3"}[sha], nil
	}
	failed := map[int64][]string{14: {"TestG"}, 12: {"TestF"}, 10: {"TestF"}, 6: {"TestA"}, 5: {"TestB", "TestC"}, 4: {"TestC"}, 3: {"TestD"}, 8: {"TestE"}}
	got, err := proven.Flakes(t.Context(), runs, failed, trees)
	require.NoError(t, err)
	var brief []string
	for _, f := range got {
		brief = append(brief, f.Test+" "+string(f.Kind))
	}
	// Most failures first, then by name. TestC failed on both runs of t45, in a row on main: red.
	// TestF failed, passed, failed on dev: intermittent. TestA: run 7 passed on t67. TestB: run 4
	// on t45 failed, on TestC alone, which is TestB passing. TestD: run 2 on t3 failed with its
	// logs gone, which says nothing: once.
	// TestG failed once and the next run on its branch passed on another tree: once, not red and
	// not on-and-off.
	assert.Equal(t, []string{"TestC red", "TestF intermittent", "TestA flaky", "TestB flaky", "TestD once", "TestG once"}, brief)
	assert.Equal(t, []string{"main"}, got[0].Branches)
	assert.Equal(t, []int64{5, 4}, []int64{got[0].Runs[0].ID, got[0].Runs[1].ID}, "newest first")
	assert.Equal(t, map[string]bool{"t45": true}, got[0].Trees)

	_, err = proven.Flakes(t.Context(), runs[7:11], failed, func(context.Context, string) (string, error) { return "", errors.New("no api") })
	assert.ErrorContains(t, err, "run 7's tree: no api")
	got, err = proven.Flakes(t.Context(), nil, nil, trees)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestTheReportIsATableFlakyFirst(t *testing.T) {
	lines := proven.Report([]proven.Failure{
		{Test: "TestB", Runs: []proven.Run{{ID: 6, URL: "u6"}, {ID: 5, URL: "u5"}}, Branches: []string{"main", "pr"}, Kind: proven.Flaky},
		{Test: "TestC", Runs: []proven.Run{{ID: 4, URL: "u4"}}, Branches: []string{"main"}, Kind: proven.Red},
		{Test: "TestF", Runs: []proven.Run{{ID: 3, URL: "u3"}}, Branches: []string{"dev"}, Kind: proven.Intermittent},
		{Test: "TestD", Runs: []proven.Run{{ID: 2, URL: "u2"}}, Branches: []string{"old"}, Kind: proven.Once},
	}, 7)
	assert.Equal(t, []string{
		"| test | failed | of runs | branches | verdict |",
		"| --- | --- | --- | --- | --- |",
		"| TestB | 2 ([6](u6) [5](u5)) | 7 | main, pr | flaky: did not fail on another run of the same tree |",
		"| TestC | 1 ([4](u4)) | 7 | main | red: failed on runs in a row |",
		"| TestF | 1 ([3](u3)) | 7 | dev | intermittent: on and off along its branch |",
		"| TestD | 1 ([2](u2)) | 7 | old | once: no other run of its tree to tell |",
	}, lines)
}
