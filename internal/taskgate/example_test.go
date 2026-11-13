package taskgate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/internal/gittest"
	"github.com/spin-stack/go-tools/internal/taskgate"
)

// The site's CI page shows site/examples/github as a repository would have it, so it is held to
// what taskgate holds such a repository to, at the limits the command's flags default to: an
// example that a gate here would fail is one nobody can copy.
// It is copied into a repository of its own, as a repository would have it: git run in this
// checkout is fenced off it (internal/gittest).
func TestTheSitesCIExamplePasses(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.CopyFS(root, os.DirFS(filepath.Join("..", "..", "site", "examples", "github"))))
	gittest.Run(t, root, "init", "-q")
	known, err := taskgate.Tasks("task", root)
	require.NoError(t, err)
	require.Contains(t, known, "mutate", "the example's Taskfile was not the one listed")

	findings, err := taskgate.Check(root, taskgate.Limits{Block: 12, Script: 40}, known)
	require.NoError(t, err)
	require.Empty(t, findings)
}
