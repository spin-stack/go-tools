package mutate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A profile names every package the binary measured, and only this package's own files are about
// the edit: a subpackage's file of the same name, or another package's, is not.
func TestAProfileIsReadForThePackagesOwnFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.cov")
	require.NoError(t, os.WriteFile(path, []byte(`mode: set
example.com/m/p/code.go:3.20,5.2 1 1
example.com/m/p/code.go:7.18,9.2 1 0
example.com/m/p/sub/code.go:11.1,12.2 1 1
example.com/m/other/code.go:13.1,14.2 1 1
example.com/m/p/code.go:15.1 1 1
example.com/m/p/code.go:x.1,16.2 1 1
example.com/m/p/code.go:17.1,y.2 1 1
`), 0o600))
	executed, all, err := readProfile(path, "example.com/m/p/")
	require.NoError(t, err)
	assert.Equal(t, map[string][]span{"code.go": {{3, 5}}}, executed)
	assert.Equal(t, map[string][]span{"code.go": {{3, 5}, {7, 9}}}, all)

	_, _, err = readProfile(filepath.Join(t.TempDir(), "none.cov"), "example.com/m/p/")
	assert.Error(t, err)
}

// A line in no block the profile counts - a switch's case expression, in some releases of Go - is
// one nobody knows who reaches, and is asked of every test rather than of none; a line in a block
// no test executed is reached by none.
func TestALineNoBlockHoldsIsAskedOfEveryTest(t *testing.T) {
	c := &Coverage{
		blocks:  map[string][]span{"code.go": {{3, 4}, {6, 6}, {8, 8}}},
		reached: map[string]map[string][]span{"code.go": {"TestB": {{3, 4}}, "TestA": {{3, 4}, {6, 6}}}},
	}
	assert.Equal(t, []string{"TestA", "TestB"}, c.Reaching("dir/code.go", 4))
	assert.Equal(t, []string{"TestA"}, c.Reaching("code.go", 6))
	assert.Equal(t, []string{}, c.Reaching("code.go", 8))
	assert.Nil(t, c.Reaching("code.go", 5))
	assert.Nil(t, c.Reaching("other.go", 4))
}
