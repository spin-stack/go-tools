package mutate_test

import (
	"testing"
	"time"

	"github.com/spin-stack/go-tools/internal/mutate"
)

// runTests is mutate.Run, failing t where the tests could not be asked at all.
func runTests(t *testing.T, m mutate.Mutation, a mutate.Asked) (mutate.Outcome, string) {
	t.Helper()
	answer, err := mutate.Run(t.Context(), m, a)
	if err != nil {
		t.Fatal(err)
	}
	return answer.Outcome, answer.Said
}

// askTests is mutate.Ask in a tree of its own, failing t where the tests could not be asked at all.
func askTests(t *testing.T, bin, dir string, id int, timeout string, only []string) (mutate.Outcome, string) {
	t.Helper()
	allowed, err := time.ParseDuration(timeout)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := mutate.Ask(t.Context(), mutate.NewTree(t.TempDir()), bin, dir, id, mutate.Allowance{Run: allowed}, only)
	if err != nil {
		t.Fatal(err)
	}
	return answer.Outcome, answer.Said
}
