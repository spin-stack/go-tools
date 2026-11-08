package taskgate_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/internal/taskgate"
)

// The site's CI page shows site/examples/github as a repository would have it, so it is held to
// what taskgate holds such a repository to, at the limits the command's flags default to: an
// example that a gate here would fail is one nobody can copy.
func TestTheSitesCIExamplePasses(t *testing.T) {
	root := filepath.Join("..", "..", "site", "examples", "github")
	known, err := taskgate.Tasks("task", root)
	require.NoError(t, err)
	require.Contains(t, known, "mutate", "the example's Taskfile was not the one listed")

	findings, err := taskgate.Check(root, taskgate.Limits{Block: 12, Script: 40}, known)
	require.NoError(t, err)
	require.Empty(t, findings)
}
