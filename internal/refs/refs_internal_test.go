package refs

import "testing"

// A glob is walked from the last directory it names before its first wildcard - docs/, and not
// the repository, for `docs/**/*` - which is what makes a globstar in a large tree cheap. The
// answer is the same from anywhere above it, so only this says where the walk starts.
func TestAGlobIsWalkedFromItsLastLiteralDirectory(t *testing.T) {
	for pattern, want := range map[string]string{
		"docs/**/*":           "docs",
		"internal/db/*.sql":   "internal/db",
		"internal/db/queries": "internal/db/queries",
		"docs*":               ".",
		"/*":                  "",
	} {
		if got := literalPrefix(pattern); got != want {
			t.Errorf("literalPrefix(%q) = %q, want %q", pattern, got, want)
		}
	}
}
