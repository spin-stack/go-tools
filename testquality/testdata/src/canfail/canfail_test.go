// Package canfail is the narrowness proof: every test here reaches a failure by one of the
// routes the analyzer has to recognise, and none of them may be flagged. A check that fires
// on these is one somebody turns off.
package canfail

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFatal(t *testing.T) {
	if 1 != 1 {
		t.Fatal("no")
	}
}

func TestErrorf(t *testing.T) {
	t.Errorf("at least it can fail")
}

// Inside a subtest, where the testing value is the closure's own and not the function's.
func TestInsideASubtest(t *testing.T) {
	t.Run("a case", func(t *testing.T) {
		t.Fatal("this is the one that fails")
	})
}

// Through a helper handed the test - which is most of this repository's tests, and the
// reason the rule is "was the test handed to something" rather than a list of names.
func TestThroughAHelper(t *testing.T) {
	mustBeTrue(t, true)
}

func mustBeTrue(t *testing.T, ok bool) {
	t.Helper()
	if !ok {
		t.Fatal("not true")
	}
}

// Through a method on a fixture that holds the test.
func TestThroughAFixtureMethod(t *testing.T) {
	f := fixture{}
	f.check(t)
}

type fixture struct{}

func (fixture) check(t *testing.T) {
	t.Helper()
	t.Log("a fixture that can fail the test")
}

// A skip inside a guard is a decision about the environment, not a test that never runs.
func TestSkippedOnlyWhenSomethingIsMissing(t *testing.T) {
	if testing.Short() {
		t.Skip("needs the slow path")
	}
	t.Log("otherwise it runs")
	if 1 != 1 {
		t.Fatal("no")
	}
}

// A benchmark asserts nothing by design: what it claims is a number.
func BenchmarkSomething(b *testing.B) {
	for b.Loop() {
		_ = 1 + 1
	}
}

// Through an assertion package, handed the test as the interface it takes: t is not what the call
// is given, and the package is what says it can fail.
func TestThroughAnAssertionPackage(t *testing.T) {
	var reporter assert.TestingT = t
	assert.True(reporter, true)
}

// Not tests, though named like them: a method, whatever its parameter, and functions whose
// parameter is a pointer to something that is not testing's T, B or F. None may be flagged. Go
// runs none of them either: a method is not a test, and after Test it wants no lowercase letter.
type suite struct{}

func (suite) TestInASuite(t *testing.T) {}

type T struct{}

func Testable(p *T) {}

func Testlike(p *[]int) {}

func Testish(p *error) {}
