// The module ghatrace gotest is tried on: a test that passes, one that says something and
// passes, and one that fails when SAMPLE_FAIL is set.
package sample

import (
	"os"
	"testing"
)

func TestPasses(t *testing.T) {}

func TestSays(t *testing.T) { t.Log("said only with -v") }

func TestFailsWhenAsked(t *testing.T) {
	if os.Getenv("SAMPLE_FAIL") != "" {
		t.Error("asked to fail")
	}
}
