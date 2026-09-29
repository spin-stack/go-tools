package allow_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spin-stack/go-tools/internal/allow"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "allow.txt")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Every key has its reason; the comments and blank lines around them are not keys.
func TestAKeyIsTheLineBeforeItsReason(t *testing.T) {
	got, err := allow.Read(write(t, "# the list\n\nCLAUDE.md\tinternal/sweep  # named to say it is gone\n  pkg:TestX # the race detector\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"CLAUDE.md\tinternal/sweep": true, "pkg:TestX": true}
	if len(got) != len(want) || !got["CLAUDE.md\tinternal/sweep"] || !got["pkg:TestX"] {
		t.Errorf("keys are %v, want %v", got, want)
	}
}

// An exception without its why is refused, whichever gate reads it, and a list named and not
// there is an error; no list named is none.
func TestAnExceptionNeedsItsReasonAndItsFile(t *testing.T) {
	for _, body := range []string{"pkg:TestX\n", "# the list\npkg:TestX #  \n"} {
		// The line is said as an editor counts it, from 1.
		want := fmt.Sprintf("allow.txt:%d has no reason", strings.Count(body, "\n"))
		if _, err := allow.Read(write(t, body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", body, err, want)
		}
	}
	if _, err := allow.Read(filepath.Join(t.TempDir(), "missing.txt")); err == nil {
		t.Error("a list that is not there was read as empty")
	}
	if got, err := allow.Read(""); err != nil || len(got) != 0 {
		t.Errorf("no path: %v, %v", got, err)
	}
}

// Both halves of the ratchet: a finding nothing explains keeps its place, and an entry that
// explains nothing is named, however many findings it would have explained.
func TestSplitIsWhatNothingExplainsAndWhatExplainsNothing(t *testing.T) {
	allowed := map[string]bool{"b": true, "a": true, "c": true}
	unexplained, stale := allow.Split(allowed, []string{"z", "c", "y", "c"}, strings.ToLower)
	if !slices.Equal(unexplained, []string{"z", "y"}) {
		t.Errorf("unexplained = %q", unexplained)
	}
	if !slices.Equal(stale, []string{"a", "b"}) {
		t.Errorf("stale = %q", stale)
	}
}
