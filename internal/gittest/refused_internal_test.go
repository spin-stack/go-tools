package gittest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A git outside the temporary directory, or one naming another repository, is refused before it
// runs; one in the temporary directory or under it, naming none, is not.
func TestAGitNamingAnotherRepositoryIsRefused(t *testing.T) {
	tmp := t.TempDir()
	for _, tc := range []struct {
		dir     string
		args    []string
		refused bool
	}{
		{os.TempDir(), []string{"status"}, false},
		{filepath.Join(tmp, "x"), []string{"-c", "user.name=x", "commit"}, false},
		{"/", []string{"status"}, true},
		{os.TempDir() + "-sibling", []string{"status"}, true},
		{tmp, []string{"-C", "/", "status"}, true},
		{tmp, []string{"--git-dir=/x/.git", "status"}, true},
		{tmp, []string{"--work-tree", "/", "status"}, true},
	} {
		assert.Equal(t, tc.refused, refused(tc.dir, tc.args) != "", "git %v in %s", tc.args, tc.dir)
	}
}
