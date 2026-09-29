package testquality_test

import (
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
