package gittest_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/internal/gittest"
)

// outer is a repository standing for the checkout a test runs in, with a directory inside it.
func outer(t *testing.T) (repo, inside string) {
	t.Helper()
	repo = t.TempDir()
	gittest.Run(t, repo, "init", "-q")
	inside = filepath.Join(repo, "tmp", "x")
	require.NoError(t, os.MkdirAll(inside, 0o700))
	return repo, inside
}

// The ways a test's author reaches the checkout, each refused: a test's own git in a directory
// that is not a repository, under a TMPDIR inside the checkout; the code under test's git run
// without its -C in a package of the checkout; and a GIT_DIR a hook left in the environment.
func TestAGitOffItsRepositoryWritesNoOtherRepositorysConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(t *testing.T, repo, inside string) ([]byte, error)
	}{
		{"a test's git under a TMPDIR inside the checkout", func(t *testing.T, repo, inside string) ([]byte, error) {
			t.Setenv("TMPDIR", filepath.Join(repo, "tmp"))
			return gittest.Command(t, inside, "config", "user.name", "Ana Lima").CombinedOutput()
		}},
		{"a test's git under a hook's GIT_DIR", func(t *testing.T, repo, inside string) ([]byte, error) {
			t.Setenv("TMPDIR", filepath.Join(repo, "tmp"))
			t.Setenv("GIT_DIR", filepath.Join(repo, ".git"))
			return gittest.Command(t, inside, "config", "user.name", "Ana Lima").CombinedOutput()
		}},
		{"the code under test's git in the checkout, fenced", func(t *testing.T, repo, inside string) ([]byte, error) {
			for _, k := range []string{"GIT_CEILING_DIRECTORIES", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM"} {
				t.Setenv(k, "")
			}
			t.Setenv("GIT_DIR", filepath.Join(repo, ".git"))
			t.Chdir(inside)
			gittest.Fence()
			return exec.CommandContext(t.Context(), "git", "config", "user.name", "Ana Lima").CombinedOutput()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, inside := outer(t)
			out, err := tc.run(t, repo, inside)
			require.Error(t, err, "git wrote: %s", out)
			assert.Contains(t, string(out), "not in a git directory")
			config, err := os.ReadFile(filepath.Join(repo, ".git", "config"))
			require.NoError(t, err)
			assert.NotContains(t, string(config), "Ana Lima")
		})
	}
}

// What a test commits says gittest's identity - an address the authors gate refuses - and not
// whoever the machine's configuration names.
func TestATestsCommitIsGittests(t *testing.T) {
	repo := t.TempDir()
	gittest.Run(t, repo, "init", "-q")
	gittest.Run(t, repo, "commit", "-q", "--allow-empty", "-m", "one")
	assert.Equal(t, gittest.IdentityEmail+" "+gittest.IdentityEmail, gittest.Run(t, repo, "log", "-1", "--format=%ae %ce"))
}
