package versions

import "testing"

// This repository is held to its own versions.yaml as any other that uses the package is. The
// README's entries and commands are examples of the format, not pins of this repository's.
func TestThisRepositoryPassesTheGate(t *testing.T) {
	g := Gate{Root: "..", Elsewhere: []string{"go.sum", "README.md", "versions/testdata/"}}
	if err := g.Check(); err != nil {
		t.Error(err)
	}
}
