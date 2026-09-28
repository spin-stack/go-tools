// Package skipped holds the tests that never run: the skip is the first thing they do, and
// nothing about a green suite says so.
package skipped

import "testing"

func TestSkippedOutright(t *testing.T) {
	t.Skip("comes back when the feature is written") // want `TestSkippedOutright is skipped unconditionally`
	t.Fatal("never reached")
}

func TestSkippedAfterSomeSetup(t *testing.T) {
	server := "https://example.invalid"
	_ = server
	t.Skipf("flaky against %s", server) // want `TestSkippedAfterSomeSetup is skipped unconditionally`
	t.Fatal("never reached either")
}

func TestSkipNow(t *testing.T) {
	t.SkipNow() // want `TestSkipNow is skipped unconditionally`
}
