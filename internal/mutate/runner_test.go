package mutate_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/internal/mutate"
)

// A cap is spread over the changed functions, one edit of each a round, so it covers as many of
// them as it can rather than the first one exhaustively; under the cap, every edit is taken.
func TestACapIsSpreadOverTheChangedFunctions(t *testing.T) {
	m := func(file, fn, what string) mutate.Mutation { return mutate.Mutation{File: file, Func: fn, What: what} }
	all := []mutate.Mutation{
		m("a.go", "F", "1"), m("a.go", "F", "2"), m("a.go", "F", "3"),
		m("a.go", "G", "1"),
		m("b.go", "F", "1"), m("b.go", "F", "2"),
	}
	key := func(ms []mutate.Mutation) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.File+":"+m.Func+":"+m.What)
		}
		return out
	}
	assert.Equal(t, []string{"a.go:F:1", "a.go:G:1", "b.go:F:1", "a.go:F:2"}, key(mutate.Spread(all, 4)),
		"a function of the same name in another file is another function, and each is taken once a round")
	assert.Equal(t, []string{"a.go:F:1", "a.go:G:1", "b.go:F:1", "a.go:F:2", "b.go:F:2", "a.go:F:3"}, key(mutate.Spread(all, 10)))
	assert.Equal(t, []string{"a.go:F:1"}, key(mutate.Spread(all, 1)))
	assert.Empty(t, mutate.Spread(all, 0))
	assert.Empty(t, mutate.Spread(nil, 3))
}

// Every edit is put to the tests and counted once, as the tests answered it, and each is said as
// it comes: Double's refused by TestDouble, Half's and Sign's let through by tests that check neither, and
// a package with no tests named rather than counted as a survivor.
func TestEveryEditIsCountedAsTheTestsAnswered(t *testing.T) {
	dir := refuses(t)
	plan, err := mutate.Plan(filepath.Join(dir, "code.go"), map[int]bool{4: true, 8: true, 13: true, 14: true})
	require.NoError(t, err)

	bare := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bare, "go.mod"), []byte("module example.com/bare\n\ngo 1.24\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(bare, "code.go"), []byte("package bare\n\nfunc Double(n int) int {\n\treturn n + n\n}\n\nfunc Wide() int64 {\n\treturn 5\n}\n"), 0o600))
	untested, err := mutate.Plan(filepath.Join(bare, "code.go"), map[int]bool{4: true, 8: true})
	require.NoError(t, err)
	require.NotEmpty(t, untested)

	chosen := slices.Concat(plan, untested)
	for i := range chosen {
		chosen[i].ID = i + 1
	}
	var said strings.Builder
	tally, err := mutate.RunAll(chosen, nil, "2m", 2, &said)
	require.NoError(t, err)

	var double, unchecked int
	for _, m := range plan {
		if m.Func == "Double" {
			double++
		} else {
			unchecked++
		}
	}
	require.Positive(t, double)
	require.GreaterOrEqual(t, unchecked, 2, "the order of the survivors needs two of them")
	assert.Equal(t, double, tally.Killed, "Double's edits refused: %s", said.String())
	assert.Len(t, tally.Survived, unchecked, "Half's and Sign's edits let through: %s", said.String())
	for _, m := range tally.Survived {
		assert.NotEqual(t, "Double", m.Func)
	}
	assert.True(t, slices.IsSortedFunc(tally.Survived, func(a, b mutate.Mutation) int { return strings.Compare(a.String(), b.String()) }),
		"the survivors are not in order: %v", tally.Survived)
	assert.Equal(t, len(untested), tally.Untested)
	assert.Equal(t, map[string]bool{bare: true}, tally.UntestedPackages)
	assert.Zero(t, tally.Unbuildable)
	assert.Equal(t, double, strings.Count(said.String(), "  refused   "))
	assert.Equal(t, unchecked, strings.Count(said.String(), "  SURVIVED  "))
	assert.Contains(t, said.String(), fmt.Sprintf("%s: %d edit(s) in one binary, 0 built alone", dir, len(plan)),
		"the package's edits were not built into its schema")
	assert.Contains(t, said.String(), bare+": 2 edit(s) in one binary, 1 built alone",
		"Wide's int64 literal is built alone, and a run of one is what is timed")
	assert.NotContains(t, said.String(), " took ", "a run under a minute was said to be slow")
}

// A test that fails with nothing broken refuses no edit: the coverage the run measures first says
// so, for an edit in the package's binary and for one built alone. Asked anyway, it would refuse
// every edit, and a change would pass the gate on a broken test.
func TestATestThatAlwaysFailsRefusesNothing(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":       "module example.com/broken\n\ngo 1.24\n",
		"code.go":      "package broken\n\nfunc One() int {\n\treturn 1\n}\n\nfunc Wide() int64 {\n\treturn 5\n}\n",
		"code_test.go": "package broken\n\nimport \"testing\"\n\nfunc TestBroken(t *testing.T) {\n\t_, _ = One(), Wide()\n\tt.Fatal(\"broken regardless\")\n}\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	plan, err := mutate.Plan(filepath.Join(dir, "code.go"), map[int]bool{4: true, 8: true})
	require.NoError(t, err)
	for i := range plan {
		plan[i].ID = i + 1
	}
	var said strings.Builder
	tally, err := mutate.RunAll(plan, nil, "2m", 2, &said)
	require.NoError(t, err)
	assert.Contains(t, said.String(), dir+": 1 edit(s) in one binary, 1 built alone", "One's literal in the binary, Wide's alone")
	assert.Zero(t, tally.Killed, "a test broken regardless refused an edit: %s", said.String())
	assert.Len(t, tally.Survived, 2, "%s", said.String())
}

// A package with no tests of its own is asked through the packages that import it, each built
// once: Double's edits are refused by the importer's test and Half's, which it never calls, are not.
func TestAPackageWithNoTestsIsAskedThroughItsImporters(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":          "module example.com/imp\n\ngo 1.24\n",
		"lib/lib.go":      "package lib\n\nfunc Double(n int) int {\n\treturn n + n\n}\n\nfunc Half(n int) int {\n\treturn n / 2\n}\n",
		"use/use.go":      "package use\n\nimport \"example.com/imp/lib\"\n\nfunc Four() int { return lib.Double(2) }\n",
		"use/use_test.go": "package use\n\nimport \"testing\"\n\nfunc TestFour(t *testing.T) {\n\tif Four() != 4 {\n\t\tt.Fatal(\"not four\")\n\t}\n}\n",
		"also/also.go":    "package also\n\nimport \"example.com/imp/lib\"\n\nvar _ = lib.Half\n",
	} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	t.Chdir(dir)
	plan, err := mutate.Plan(filepath.Join("lib", "lib.go"), map[int]bool{4: true, 8: true})
	require.NoError(t, err)
	var double, half int
	for i := range plan {
		plan[i].ID = i + 1
		if plan[i].Func == "Double" {
			double++
		} else {
			half++
		}
	}
	require.Positive(t, double)
	require.Positive(t, half)

	var said strings.Builder
	tally, err := mutate.RunAll(plan, map[string][]string{"lib": {"./use", "./also"}}, "2m", 2, &said)
	require.NoError(t, err)
	assert.Equal(t, double, tally.Killed, "Double's edits refused by the importer: %s", said.String())
	assert.Len(t, tally.Survived, half, "Half's edits, which no importer's test reaches: %s", said.String())
	assert.Zero(t, tally.Untested, "a package whose importers have tests was said untested")
}

// An edit the run cannot even build a way to ask is an error, not an outcome: a file that is not
// there has no overlay.
func TestAnEditThatCannotBeAskedIsAnError(t *testing.T) {
	gone := mutate.Mutation{ID: 1, File: filepath.Join(t.TempDir(), "gone.go"), Func: "F", What: "+ becomes -"}
	_, err := mutate.RunAll([]mutate.Mutation{gone}, nil, "1m", 1, &strings.Builder{})
	assert.Error(t, err)
}
