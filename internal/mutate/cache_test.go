package mutate_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/internal/mutate"
)

// An edit the tests refused is not asked again of the same code, and is asked again once
// anything its tests are built from has changed: a test that stopped holding it is a survivor,
// not a cached refusal. A package its tests do not reach changes nothing.
func TestARefusalIsKeptForTheCodeItWasAskedOf(t *testing.T) {
	dir := refuses(t)
	lookup := cachedRun(t, doubling(t, dir))

	fresh, known := lookup()
	require.Len(t, fresh, 1, "an edit never asked was taken from the cache")
	assert.Empty(t, known)
	fresh, known = lookup()
	assert.Empty(t, fresh, "an edit refused of this code was asked again")
	assert.Len(t, known, 1)

	other := filepath.Join(dir, "other")
	require.NoError(t, os.MkdirAll(other, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(other, "other.go"), []byte("package other\n\nfunc Other() int { return 1 }\n"), 0o600))
	fresh, _ = lookup()
	assert.Empty(t, fresh, "a package the tests do not reach made a refusal stale")

	// Its testdata first, which leaves the test refusing the edit: kept again, under the new key.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "testdata"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "testdata", "in.txt"), []byte("read by a test\n"), 0o600))
	fresh, known = lookup()
	assert.Len(t, fresh, 1, "a refusal was kept after its package's testdata changed")
	assert.Empty(t, known)

	// Then the test, weakened until it lets the edit through.
	test := filepath.Join(dir, "code_test.go")
	body, err := os.ReadFile(test)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(test, []byte(strings.Replace(string(body), "Double(2) != 4", "Double(2) == 5", 1)), 0o600))
	fresh, known = lookup()
	assert.Nil(t, fresh, "the weakened test was answered from the cache rather than found to let the edit through")
	assert.Nil(t, known)
}

// A refusal is of the packages the tests are built from too, however far down: one the tests
// import, which imports another that changes, can change what the tests answer.
func TestARefusalIsKeptForThePackagesItsTestsAreBuiltFrom(t *testing.T) {
	// The package one directory into its module, as most are: the module's own files are then in
	// no package's directory, and only the module's pins can say they changed.
	dir := refuses(t)
	root := filepath.Dir(dir) // the test's own temporary directory, which dir is made in
	require.NoError(t, os.Remove(filepath.Join(dir, "go.mod")))
	for name, body := range map[string]string{
		"go.mod":            "module example.com/m\n\ngo 1.24\n",
		"dep/dep.go":        "package dep\n\nimport \"example.com/m/dep/deeper\"\n\nfunc Two() int { return deeper.Two() }\n",
		"dep/deeper/two.go": "package deeper\n\nfunc Two() int { return 2 }\n",
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(body), 0o600))
	}
	test := filepath.Join(dir, "code_test.go")
	body, err := os.ReadFile(test)
	require.NoError(t, err)
	body = []byte(strings.Replace(strings.Replace(string(body), "\t\"os\"\n", "\t\"os\"\n\n\t\"example.com/m/dep\"\n", 1),
		"Double(2) != 4", "Double(dep.Two()) != 4", 1))
	require.NoError(t, os.WriteFile(test, body, 0o600))
	lookup := cachedRun(t, doubling(t, dir))

	fresh, _ := lookup()
	require.Len(t, fresh, 1)
	fresh, known := lookup()
	require.Empty(t, fresh, "an edit refused of this code was asked again")
	require.Len(t, known, 1)

	require.NoError(t, os.WriteFile(filepath.Join(root, "dep", "deeper", "two.go"), []byte("package deeper\n\nfunc Two() int { return 1 + 1 }\n"), 0o600))
	fresh, known = lookup()
	assert.Len(t, fresh, 1, "a refusal was kept after a package the tests are built from changed")
	assert.Empty(t, known)

	// go.mod pins every module outside this one the tests are built from.
	mod := filepath.Join(root, "go.mod")
	pinned, err := os.ReadFile(mod)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(mod, append(pinned, "\ntoolchain go1.24.0\n"...), 0o600))
	fresh, known = lookup()
	assert.Len(t, fresh, 1, "a refusal was kept after the module's pins changed")
	assert.Empty(t, known)
}

// cachedRun is a run over m with a cache that outlives it, as a gate run again keeps one: each
// call opens the cache anew, so nothing is remembered but what is on disk, asks the tests about m
// when the cache does not answer, and says which it was - nil, nil when the tests let m through.
func cachedRun(t *testing.T, m mutate.Mutation) func() (fresh, known []mutate.Mutation) {
	t.Helper()
	store := t.TempDir()
	m.ID = 1
	return func() (fresh, known []mutate.Mutation) {
		t.Helper()
		c, err := mutate.OpenCache(store)
		require.NoError(t, err)
		fresh, known, err = c.Known([]mutate.Mutation{m}, nil)
		require.NoError(t, err)
		if len(fresh) > 0 {
			tally, err := mutate.RunAll(fresh, nil, "2m", 1, io.Discard)
			require.NoError(t, err)
			require.NoError(t, c.Keep(tally.Refused))
			if len(tally.Survived) > 0 {
				return nil, nil
			}
		}
		return fresh, known
	}
}
