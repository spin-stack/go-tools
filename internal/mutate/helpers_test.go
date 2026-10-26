package mutate_test

import (
	"testing"

	"github.com/spin-stack/go-tools/internal/mutate"
)

// runTests is mutate.Run, failing t where the tests could not be asked at all.
func runTests(t *testing.T, m mutate.Mutation, a mutate.Asked) (mutate.Outcome, string) {
	t.Helper()
	outcome, out, err := mutate.Run(m, a)
	if err != nil {
		t.Fatal(err)
	}
	return outcome, out
}

// askTests is mutate.Ask in a tree of its own, failing t where the tests could not be asked at all.
func askTests(t *testing.T, bin, dir string, id int, timeout string, only []string) (mutate.Outcome, string) {
	t.Helper()
	outcome, out, err := mutate.Ask(mutate.NewTree(t.TempDir()), bin, dir, id, timeout, only)
	if err != nil {
		t.Fatal(err)
	}
	return outcome, out
}
