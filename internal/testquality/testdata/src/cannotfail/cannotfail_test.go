// Package cannotfail holds the tests that must be flagged: nothing in any of them can make
// the test fail, so each passes forever and reports this package as covered.
package cannotfail

import (
	"errors"
	"testing"
)

func TestNothingAtAll(t *testing.T) { // want `TestNothingAtAll cannot fail`
	_ = 1 + 1
}

func TestOnlyLogs(t *testing.T) { // want `TestOnlyLogs cannot fail`
	// The runbook shape: it says what somebody should check, and checks nothing.
	t.Log("the operator confirms the workspace came up")
	t.Logf("and that it has %d disks", 2)
}

func TestOnlyHelpersThatNeverSeeTheTest(t *testing.T) { // want `TestOnlyHelpersThatNeverSeeTheTest cannot fail`
	// A helper that cannot fail the test is not an assertion, however much it computes.
	if double(2) != 4 {
		_ = "nothing happens here either"
	}
}

func double(n int) int { return n * 2 }

func TestSubtestsThatAssertNothing(t *testing.T) { // want `TestSubtestsThatAssertNothing cannot fail`
	for _, name := range []string{"a", "b"} {
		t.Run(name, func(t *testing.T) {
			_ = name
		})
	}
}

// Calls that are neither the test's nor an assertion package's, handed values that are not the
// test: a method of the universe's error, a function in a field, a pointer to a slice, to an
// error, and to a type that is only named like testing's.
type T struct{}

type holder struct{ check func(any) }

func TestCallsThatAreNotAssertions(t *testing.T) { // want `TestCallsThatAreNotAssertions cannot fail`
	err := errors.New("x")
	_ = err.Error()
	h := holder{check: func(any) {}}
	h.check(&[]int{})
	h.check(&err)
	h.check(&T{})
}
