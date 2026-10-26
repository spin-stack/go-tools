package mutate

import "testing"

// runTests is Run, failing t where the tests could not be asked at all.
func runTests(t *testing.T, m Mutation, a Asked) (Outcome, string) {
	t.Helper()
	outcome, out, err := Run(m, a)
	if err != nil {
		t.Fatal(err)
	}
	return outcome, out
}

// askTests is Ask in a tree of its own, failing t where the tests could not be asked at all.
func askTests(t *testing.T, bin, dir string, id int, timeout string, only []string) (Outcome, string) {
	t.Helper()
	outcome, out, err := Ask(NewTree(t.TempDir()), bin, dir, id, timeout, only)
	if err != nil {
		t.Fatal(err)
	}
	return outcome, out
}
