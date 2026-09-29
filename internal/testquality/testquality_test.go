package testquality_test

import (
	"maps"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/spin-stack/go-tools/internal/testquality"
)

func TestTestsThatCannotFailAreFlagged(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), testquality.Analyzer, "cannotfail")
}

// Narrowness: every route to failure this repository uses produces no diagnostic.
func TestTestsThatCanFailAreNotFlagged(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), testquality.Analyzer, "canfail")
}

func TestTestsThatNeverRunAreFlagged(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), testquality.Analyzer, "skipped")
}

// The keys are what every repository's allowlist is written in: the package as a person names it,
// whether the test is in the package or beside it, and a test behind a build tag held too.
func TestFindingsAreKeyedByThePackageAPersonWrites(t *testing.T) {
	t.Chdir(filepath.Join("testdata", "mod"))

	findings, err := testquality.Findings("./...")
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	for _, f := range findings {
		got[f.Key()] = filepath.Base(f.Pos.Filename)
	}
	want := map[string]string{
		"example.com/mod/pkg:TestInternal": "internal_test.go",
		"example.com/mod/pkg:TestExternal": "external_test.go",
		"example.com/mod/pkg:TestTagged":   "tagged_test.go",
		"example.com/mod/pkg:TestEndToEnd": "e2e_test.go",
	}
	if !maps.Equal(got, want) {
		t.Errorf("findings = %v, want %v", got, want)
	}
}
