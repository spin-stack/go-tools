package mutate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/mutate"
)

// schemaModule is a module of its own with an edit of every kind a schema builds, each held by
// a test, and one it cannot build: a literal an int64 must be.
func schemaModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod": "module example.com/schema\n\ngo 1.24\n",
		"code.go": `package schema

type counter struct {
	n, a, b int
	last    int
}

func (c *counter) Mark(v int) {
	c.last = v
	c.n = c.n + 1
	c.a, c.b = pair(v)
	c.b += 1
}

func pair(v int) (int, int) { return v, v }

func Both(a, b bool) bool {
	return a &&
		b
}

func Less(a, b int) bool {
	return a < b
}

func Either(f, g func() bool) bool {
	return f() || g()
}

func Sum(a, b, c int) int {
	return a + b + c
}

func Scale(a int) int {
	return a * 2
}

func Wide() int64 {
	var n int64 = 5
	return n
}
`,
		"code_test.go": `package schema

import "testing"

func TestMark(t *testing.T) {
	var c counter
	c.Mark(7)
	if c.n != 1 || c.last != 7 || c.a != 7 || c.b != 8 {
		t.Fatalf("marked %+v", c)
	}
}

func TestBothNeither(t *testing.T) {
	if Both(false, false) {
		t.Fatal("neither")
	}
}

func TestBothOne(t *testing.T) {
	if Both(true, false) {
		t.Fatal("one")
	}
}

func TestBothBoth(t *testing.T) {
	if !Both(true, true) {
		t.Fatal("both")
	}
}

func TestSum(t *testing.T) {
	if Sum(1, 2, 4) != 7 {
		t.Fatal("sum")
	}
}

func TestScale(t *testing.T) {
	if Scale(3) != 6 {
		t.Fatal("scale")
	}
}

func TestLess(t *testing.T) {
	if !Less(1, 2) || Less(2, 2) {
		t.Fatal("less")
	}
}

func TestEither(t *testing.T) {
	calls := 0
	g := func() bool { calls++; return true }
	if !Either(func() bool { return true }, g) || calls != 0 {
		t.Fatalf("either called g %d times", calls)
	}
	if Either(func() bool { return false }, func() bool { return false }) {
		t.Fatal("either")
	}
}

func TestWide(t *testing.T) {
	if Wide() != 5 {
		t.Fatal("wide")
	}
}
`,
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	return dir
}

// Every edit, switched on in the one binary its package's edits share, is refused or let through
// as it is built alone, by the same test first - the schema is a cheaper way to ask the same
// question, and no other one - and with none switched on the binary is the package as it is. An
// edit the schema cannot build is built alone, and the rest still share the binary; the lines the
// binary counts are the file's own, so an edit is still put to the tests that reach its line, and
// only to those it is put to.
func TestAnEditInTheSchemaIsAnsweredAsItIsAlone(t *testing.T) {
	dir := schemaModule(t)
	file := filepath.Join(dir, "code.go")
	all := map[int]bool{}
	for i := range 60 {
		all[i+1] = true
	}
	plan, err := mutate.Plan(file, all)
	require.NoError(t, err)
	var planned []string
	for i := range plan {
		plan[i].ID = i + 1
		planned = append(planned, plan[i].Func+": "+plan[i].What)
	}
	assert.Equal(t, schemaPlan, planned, "the edits planned")

	s, err := mutate.Schemata(dir, plan, t.TempDir())
	require.NoError(t, err)
	require.NotEmpty(t, s.Binary, "the package's tests were not built")
	var alone []string
	for _, m := range s.Alone {
		alone = append(alone, m.Func+": "+m.What)
	}
	assert.ElementsMatch(t, []string{"Less: the result is its left side alone", "Wide: 5 becomes 6"}, alone,
		"the edits built alone: the one no schema can say, and the literal an int64 must be")
	assert.Equal(t, "6", wide(t, plan).With, "the literal alone is not what its edit says")
	assert.Len(t, s.Built, len(plan)-len(s.Alone))

	outcome, out := mutate.Ask(s.Binary, dir, 0, "2m", nil)
	require.Equal(t, mutate.Survived, outcome, "with no edit on, the package failed its own tests: %s", out)

	covered, err := mutate.Cover(dir, "2m", make(chan struct{}, 2))
	require.NoError(t, err)
	killed := 0
	for _, m := range s.Built {
		inSchema, said := mutate.Ask(s.Binary, dir, m.ID, "2m", nil)
		reaching, _ := mutate.Ask(s.Binary, dir, m.ID, "2m", covered.Reaching(m.File, m.Line))
		assert.Equal(t, inSchema, reaching, "%s: the tests that reach its line in the file answer otherwise in the schema", m)
		nobody, _ := mutate.Ask(s.Binary, dir, m.ID, "2m", []string{"TestNothingIsCalledThis"})
		assert.Equal(t, mutate.Survived, nobody, "%s: tests that were not named ran", m)
		overlay, err := mutate.Overlay(m, t.TempDir())
		require.NoError(t, err)
		built, by := mutate.Run(m, mutate.Asked{Overlay: overlay, Timeout: "2m"})
		assert.Equal(t, built, inSchema, "%s: alone %s, in the schema %s", m, by, said)
		assert.Equal(t, firstFailure(by), firstFailure(said), "%s: another test refused it in the schema", m)
		if inSchema == mutate.Killed {
			killed++
		}
	}
	assert.Positive(t, killed, "no edit in the schema was refused: the switches switched nothing on")
}

// schemaPlan is every edit of schemaModule's code, in the order Plan gives them. Two fields written
// at once, and one added to, are not a field a function leaves unwritten; a product is no operator
// this tool swaps, and its left side alone is not asked either.
var schemaPlan = []string{
	"Mark: the field is not written", "Mark: the field is not written", "Mark: + becomes -", "Mark: 1 becomes 2",
	"Mark: 1 becomes 2",
	"Both: the result is its left side alone", "Both: && becomes ||",
	"Less: the result is its left side alone", "Less: < becomes >=",
	"Either: the result is its left side alone", "Either: || becomes &&",
	"Sum: the result is its left side alone", "Sum: + becomes -", "Sum: + becomes -",
	"Scale: 2 becomes 3",
	"Wide: 5 becomes 6",
}

// One edit is a schema too.
func TestOneEditIsASchemaToo(t *testing.T) {
	dir := schemaModule(t)
	plan, err := mutate.Plan(filepath.Join(dir, "code.go"), map[int]bool{35: true})
	require.NoError(t, err)
	require.Equal(t, []string{"Scale: 2 becomes 3"}, whats(plan), "the plan of Scale's line")
	plan[0].ID = 1
	s, err := mutate.Schemata(dir, plan, t.TempDir())
	require.NoError(t, err)
	assert.Len(t, s.Built, 1)
	assert.NotEmpty(t, s.Binary, "the one edit was not built")
}

// firstFailure is the test that failed first in a test run's output, or empty where none did.
func firstFailure(out string) string {
	_, rest, ok := strings.Cut(out, "--- FAIL: ")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, " ")
	return name
}

// A build that fails where no edit is - another file of the package - is not the schema's to
// answer, and every edit is built alone, where it fails or not on its own; none is left in a
// binary that was never built.
func TestABuildThatFailsWhereNoEditIsBuildsEachAlone(t *testing.T) {
	dir := schemaModule(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "other.go"), []byte("package schema\n\nvar _ = undefined\n"), 0o600))
	file := filepath.Join(dir, "code.go")
	plan, err := mutate.Plan(file, map[int]bool{19: true})
	require.NoError(t, err)
	require.NotEmpty(t, plan)
	for i := range plan {
		plan[i].ID = i + 1
	}
	s, err := mutate.Schemata(dir, plan, t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, s.Built, "edits were left in a schema that does not build")
	assert.Empty(t, s.Binary)
	assert.Len(t, s.Alone, len(plan))
}

// A package whose tests are all of another build has no binary to ask, and its edits are still
// in the schema, for its importers' binaries.
func TestAPackageWhoseTestsAreOfAnotherBuildHasNoBinary(t *testing.T) {
	dir := schemaModule(t)
	test := filepath.Join(dir, "code_test.go")
	body, err := os.ReadFile(test)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(test, append([]byte("//go:build never\n\n"), body...), 0o600))
	plan, err := mutate.Plan(filepath.Join(dir, "code.go"), map[int]bool{31: true})
	require.NoError(t, err)
	require.NotEmpty(t, plan)
	for i := range plan {
		plan[i].ID = i + 1
	}
	s, err := mutate.Schemata(dir, plan, t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, s.Binary, "a binary of no tests was named")
	assert.NotEmpty(t, s.Built)
	assert.NotEmpty(t, s.Overlay)
}

// A package's tests run in its directory, where they read what is beside them: TestHalf reads
// code.go, and asked from anywhere else it fails.
func TestAnEditIsAskedInItsPackagesDirectory(t *testing.T) {
	dir := refuses(t)
	bin := filepath.Join(t.TempDir(), "pkg.test")
	build := exec.Command("go", "test", "-c", "-o", bin, ".")
	build.Dir = dir
	out, err := build.CombinedOutput()
	require.NoError(t, err, "%s", out)
	outcome, said := mutate.Ask(bin, dir, 0, "2m", []string{"TestHalf"})
	assert.Equal(t, mutate.Survived, outcome, "TestHalf did not run beside code.go: %s", said)
}

// An importer is named from the module's root and built from there, wherever this runs - the
// tests here run in mutate/ - and runs in its own directory. One with no tests has no binary.
func TestAnImportersTestsAreBuiltFromTheModulesRoot(t *testing.T) {
	work := t.TempDir()
	overlay := filepath.Join(work, "overlay.json")
	require.NoError(t, os.WriteFile(overlay, []byte(`{"Replace":{}}`), 0o600))
	root, err := filepath.Abs("..")
	require.NoError(t, err)

	bin, dir, err := mutate.ImporterBinary("./mutate", overlay, filepath.Join(work, "mutate.test"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(work, "mutate.test"), bin)
	assert.FileExists(t, bin)
	assert.Equal(t, filepath.Join(root, "mutate"), dir)

	bin, dir, err = mutate.ImporterBinary("./cmd/mutate", overlay, filepath.Join(work, "cmd.test"))
	require.NoError(t, err)
	assert.Empty(t, bin, "a package with no tests was given a binary")
	assert.Empty(t, dir)

	_, _, err = mutate.ImporterBinary("./mutate/nothing-here", overlay, filepath.Join(work, "none.test"))
	assert.Error(t, err, "a package that is not there was built")
}

// wide is the edit to the literal Wide's int64 is: 5 becomes 6.
func wide(t *testing.T, plan []mutate.Mutation) mutate.Mutation {
	t.Helper()
	for _, m := range plan {
		if m.Func == "Wide" && strings.Contains(m.What, "5 becomes 6") {
			return m
		}
	}
	t.Fatalf("no edit to Wide's literal: %v", whats(plan))
	return mutate.Mutation{}
}
