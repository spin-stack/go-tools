// The module ghatrace gotest is tried on: a test that passes, one that says something and
// passes, one that fails when SAMPLE_FAIL is set, and one that fails when -json reached GOFLAGS,
// which every go command a test runs would inherit.
package sample

import (
	"os"
	"strings"
	"testing"
)

func TestPasses(t *testing.T) {}

func TestSays(t *testing.T) { t.Log("said only with -v") }

func TestFailsWhenAsked(t *testing.T) {
	if os.Getenv("SAMPLE_FAIL") != "" {
		t.Error("asked to fail")
	}
}

func TestGoCommandsItRunsWriteNoJSON(t *testing.T) {
	if strings.Contains(os.Getenv("GOFLAGS"), "-json") {
		t.Errorf("GOFLAGS=%s: a go command this test ran would answer in JSON", os.Getenv("GOFLAGS"))
	}
}
