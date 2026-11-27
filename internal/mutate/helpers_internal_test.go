package mutate

import (
	"testing"
	"time"
)

// runTests is Run, failing t where the tests could not be asked at all.
func runTests(t *testing.T, m Mutation, a Asked) (Outcome, string) {
	t.Helper()
	answer, err := Run(t.Context(), m, a)
	if err != nil {
		t.Fatal(err)
	}
	return answer.Outcome, answer.Said
}

// askTests is Ask in a tree of its own, failing t where the tests could not be asked at all.
func askTests(t *testing.T, bin, dir string, id int, timeout string, only []string) (Outcome, string) {
	t.Helper()
	allowed, err := time.ParseDuration(timeout)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := Ask(t.Context(), NewTree(t.TempDir()), bin, dir, id, Allowance{Run: allowed}, only)
	if err != nil {
		t.Fatal(err)
	}
	return answer.Outcome, answer.Said
}
