package gate

import "testing"

// This repository is held to its own versions.yaml as any other that uses the package is. The
// site's entries and commands, and the gate's own tests, are examples of the format, not pins
// of this repository's; provenance's golden is a predicate of example pins.
func TestThisRepositoryPassesTheGate(t *testing.T) {
	g := Gate{Root: "../..", Elsewhere: []string{"go.sum", "site/src/content/docs/", "versions/gate/gate_test.go", "versions/testdata/", "provenance/testdata/"}}
	if err := g.Check(); err != nil {
		t.Error(err)
	}
}
