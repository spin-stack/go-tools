package mutate_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/internal/mutate"
)

// fixture is the sample as a file this tool can read, in a directory of its own.
func fixture(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("testdata", "sample.go.txt"))
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "sample.go")
	require.NoError(t, os.WriteFile(path, source, 0o600))
	return path
}

func whats(ms []mutate.Mutation) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Func+": "+m.What)
	}
	return out
}

// The gate is pointed at what a change touched: a function the diff does not reach is not a
// question this change has to answer, and mutating the tree is what makes this too slow to run.
func TestOnlyTheFunctionsAChangeReachesAreBroken(t *testing.T) {
	path := fixture(t)
	// Line 12 is `p.marks = p.marks + 1`, inside Mark.
	plan, err := mutate.Plan(path, map[int]bool{12: true})
	require.NoError(t, err)
	require.NotEmpty(t, plan)
	for _, m := range plan {
		assert.Equal(t, "Mark", m.Func, "a function the change does not touch was broken: %s", m)
	}
}

// What each edit changes is a decision the code makes, which is what a test can refuse.
func TestTheEditsChangeWhatTheCodeDecides(t *testing.T) {
	path := fixture(t)
	plan, err := mutate.Plan(path, map[int]bool{9: true, 12: true})
	require.NoError(t, err)
	got := whats(plan)
	assert.Contains(t, got, "Mark: < becomes >=", "the comparison that decides whether time moved")
	assert.Contains(t, got, "Mark: the field is not written", "the mark a call leaves behind")
	assert.Contains(t, got, "Mark: 1 becomes 2", "the amount a counter counts by")
	// In the order of the file: two runs over one change plan the same edits in the same order,
	// so a capped run is repeatable and its report reads top to bottom.
	for i := 1; i < len(plan); i++ {
		assert.LessOrEqual(t, plan[i-1].Line, plan[i].Line, "the plan is not in the order of the file: %v", got)
	}
}

// A line that says why an edit there would mean nothing is left alone, the way the other gates
// in this tree take a reason on the line rather than a list somewhere else.
func TestALineThatSaysWhyIsLeftAlone(t *testing.T) {
	path := fixture(t)
	all := map[int]bool{}
	for i := range 40 {
		all[i+1] = true
	}
	plan, err := mutate.Plan(path, all)
	require.NoError(t, err)
	lines := map[int]bool{}
	for _, m := range plan {
		lines[m.Line] = true
	}
	// Bounded's reason is three lines (21-23), so what it covers is line 24, under the last of
	// them; Clamp's is one line (31), over line 32. Both are asked about in one fixture because a
	// rule that holds for one shape can be wrong for the other and still pass a test of one.
	assert.False(t, lines[24], "the line under a three-line reason was broken")
	assert.False(t, lines[32], "the line under a one-line reason was broken")
	// And an exemption covers its line, not its function or the lines after it: Bounded's own
	// first comparison (17) and the line under Clamp's exempted one (33) are still asked about.
	assert.True(t, lines[17], "a line above the exemption was left alone too")
	assert.True(t, lines[33], "a line under the exempted one was left alone too")
}

// Some edits change nothing a caller can observe, whatever the tests: a capacity or a size hint,
// a buffer that is already asynchronous grown by one, a duration's count, the condition of a
// branch that only logs. They are not made, rather than made and answered with a comment on
// every line that has one; and what sits beside each - a length, an unbuffered channel, a branch
// that does something - still is.
func TestAnEditThatMeansNothingIsNotMade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "equivalent.go")
	require.NoError(t, os.WriteFile(path, []byte(`package p

func F(ctx context.Context, xs []int, err error) (int, chan int) {
	out := make([]int, 0, 8)
	seen := make(map[int]bool, 16)
	done := make(chan int, 1)
	handoff := make(chan int, 0)
	wait := 5 * time.Second
	if err != nil {
		log.G(ctx).WithError(err).Warn("ignored")
	}
	if err != nil {
		logger.Warn("refused")
		return 0, nil
	}
	if len(xs) > 3 {
		out = append(out, 1)
	}
	_, _, _ = seen, wait, handoff
	return len(out) + 2, done
}

func G(ctx context.Context, err error, c clock, done chan int) map[int]bool {
	m := make(map[int]bool)
	d := 2 * c.Second
	if err != nil {
		logger.WithError(err).Warn("ignored")
	}
	if err == nil {
		<-done
	}
	if d > 0 {
		tracker.Warn("not a log")
	}
	if len(m) > 1 {
		cleanup()
	}
	return m
}
`), 0o600))
	all := map[int]bool{}
	for i := range 40 {
		all[i+1] = true
	}
	plan, err := mutate.Plan(path, all)
	require.NoError(t, err)
	at := map[int][]string{}
	for _, m := range plan {
		at[m.Line] = append(at[m.Line], m.What)
	}

	assert.Equal(t, []string{"0 becomes 1"}, at[4], "a slice's length is asked about, its capacity is not")
	assert.Empty(t, at[5], "a map's size hint was broken")
	assert.Empty(t, at[6], "a buffer of one grown to two was broken")
	assert.Equal(t, []string{"0 becomes 1"}, at[7], "an unbuffered channel made asynchronous is a real edit")
	assert.Empty(t, at[8], "a duration's count was broken")
	assert.Empty(t, at[9], "the condition of a branch that only logs was broken")
	assert.Contains(t, at[12], "!= becomes ==", "a branch that logs and returns was left alone")
	assert.Contains(t, at[16], "> becomes <=")
	assert.Contains(t, at[16], "3 becomes 4")

	assert.Contains(t, at[25], "2 becomes 3", "a count of something that is not a time unit was left alone")
	assert.Empty(t, at[26], "the condition of a branch that only logs through a logger was broken")
	assert.Contains(t, at[29], "== becomes !=", "a branch that receives was taken for one that logs")
	assert.Contains(t, at[32], "> becomes <=", "a Warn on something that is not a logger was taken for a log line")
	assert.Contains(t, at[35], "> becomes <=", "a branch that calls something else was taken for one that logs")
}

// A reason is for an edit this tool would make. One over a line it makes none on - a capacity, a
// branch that only logs, a line in a function a lane holds, code that moved away from under it -
// explains nothing, and is reported so it goes, the way a stale line in the deadcode list does.
func TestAReasonOverNothingThisToolWouldBreakIsStale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reasons.go")
	require.NoError(t, os.WriteFile(path, []byte(`package p

func F(ctx context.Context, n int) map[int]bool {
	out := make(map[int]bool, 8) // mutate-exempt: a size hint

	if n > 3 { // mutate-exempt: a real edit, said to mean nothing
		n = 3
	}
	// mutate-exempt: a branch that only logs
	if n == 0 {
		log.G(ctx).Warn("empty")
	}
	return out
}

// mutate-lane: TestBoots, in the VM lane.
func G(n int) bool {
	return n > 1 // mutate-exempt: inside what a lane holds
}

func H(n int) bool {
	ok := n > 2 // mutate-exempt: over a comparison, and a reason
	// that goes on under it
	_ = ok
	// mutate-exempt: over the line under this one
	return n < 9
}
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(path), "boots_integration_test.go"),
		[]byte("//go:build integration\n\npackage p\n\nimport \"testing\"\n\nfunc TestBoots(t *testing.T) {}\n"), 0o600))

	stale, err := mutate.StaleExempts(path)
	require.NoError(t, err)
	assert.Equal(t, []int{4, 9, 18}, stale, "the size hint, the logging branch and the lane's line explain nothing; the comparison's does")
}

// A function held by a lane this run cannot be - root and KVM, as starting a machine is - says
// so in its doc comment, with the test that holds it; it is left out whole, and a function
// beside it is still broken. A marker naming a test its package does not have fails the plan.
func TestAFunctionHeldByALaneNamesItsTest(t *testing.T) {
	dir := t.TempDir()
	code := "package launch\n\n" +
		"// Start boots.\n//\n// mutate-lane: TestBoots, in the VM lane.\nfunc Start(a, b int) bool {\n\treturn a < b\n}\n\n" +
		"func Other(a, b int) bool {\n\treturn a < b\n}\n"
	path := filepath.Join(dir, "launch.go")
	require.NoError(t, os.WriteFile(path, []byte(code), 0o600))
	all := map[int]bool{}
	for i := range 20 {
		all[i+1] = true
	}

	_, err := mutate.Plan(path, all)
	require.ErrorContains(t, err, "TestBoots", "a marker naming a test nobody wrote was taken")

	// A marker that names no test at all is refused too, whatever it says instead.
	for _, says := range []string{"the VM lane", ""} {
		vague := filepath.Join(t.TempDir(), "launch.go")
		require.NoError(t, os.WriteFile(vague, []byte("package launch\n\n// mutate-lane: "+says+"\nfunc Start(a, b int) bool {\n\treturn a < b\n}\n"), 0o600))
		_, err := mutate.Plan(vague, all)
		require.ErrorContains(t, err, "names no test", "a marker saying %q was taken", says)
	}

	require.NoError(t, os.WriteFile(filepath.Join(dir, "launch_integration_test.go"),
		[]byte("//go:build integration\n\npackage launch\n\nimport \"testing\"\n\nfunc TestBoots(t *testing.T) {}\n"), 0o600))
	plan, err := mutate.Plan(path, all)
	require.NoError(t, err)
	for _, m := range plan {
		assert.NotEqual(t, "Start", m.Func, "a function held by a lane was broken: %s", m.What)
	}
	assert.NotEmpty(t, plan, "the function beside it was left alone too")
}

// What wires a binary together is held by the lane that runs the binary, in a package of its
// own, and the marker names it from the module's root. That test has to be there too.
func TestAFunctionHeldByAnotherPackagesLaneNamesItFromTheRoot(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module m\n"), 0o600))
	lane := filepath.Join(root, "lane", "e2e")
	require.NoError(t, os.MkdirAll(lane, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(lane, "boot_test.go"),
		[]byte("package e2e\n\nimport \"testing\"\n\nfunc TestBoots(t *testing.T) {}\n"), 0o600))
	cmd := filepath.Join(root, "cmd", "runner")
	require.NoError(t, os.MkdirAll(cmd, 0o750))
	all := map[int]bool{}
	for i := range 20 {
		all[i+1] = true
	}

	for marker, held := range map[string]bool{"lane/e2e.TestBoots": true, "lane/e2e.TestNobodyWrote": false} {
		path := filepath.Join(cmd, "main.go")
		require.NoError(t, os.WriteFile(path, []byte("package main\n\n// mutate-lane: "+marker+", which runs the binary.\n"+
			"func run(a, b int) bool {\n\treturn a < b\n}\n"), 0o600))
		plan, err := mutate.Plan(path, all)
		if !held {
			require.ErrorContains(t, err, "TestNobodyWrote", "a marker naming a test the other package does not have was taken")
			continue
		}
		require.NoError(t, err)
		assert.Empty(t, plan, "a function another package's lane holds was broken")
	}
}

// An edit is built in through an overlay: those bytes changed and no others, in a copy the overlay
// puts in the file's place, and the file itself is never written.
func TestAnEditIsBuiltInWithoutTouchingTheFile(t *testing.T) {
	path := fixture(t)
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	plan, err := mutate.Plan(path, map[int]bool{9: true})
	require.NoError(t, err)
	var swap mutate.Mutation
	for _, m := range plan {
		if strings.Contains(m.What, "< becomes >=") {
			swap = m
		}
	}
	require.NotEmpty(t, swap.What)

	overlay, err := mutate.Overlay(swap, t.TempDir())
	require.NoError(t, err)
	raw, err := os.ReadFile(overlay)
	require.NoError(t, err)
	var o struct {
		Replace map[string]string `json:"Replace"`
	}
	require.NoError(t, json.Unmarshal(raw, &o))
	abs, err := filepath.Abs(path)
	require.NoError(t, err)
	require.Contains(t, o.Replace, abs, "the overlay does not put the copy in the file's place")
	mutated, err := os.ReadFile(o.Replace[abs])
	require.NoError(t, err)
	assert.Contains(t, string(mutated), "if now >= p.last {", "the edit is not in the copy")
	assert.Len(t, strings.Split(string(mutated), "\n"), len(strings.Split(string(before), "\n")),
		"the edit moved the lines under it")

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "the file itself was written")
}

// git is where a change is, so what this reads is a repository: the lines a diff touches, the
// files it does not, and the file that is new and therefore all of it.
func TestTheLinesAChangeTouchesAreTheOnesItChanged(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("testdata", "sample.go.txt"))
	require.NoError(t, err)

	dir := t.TempDir()
	t.Chdir(dir)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")

	require.NoError(t, os.WriteFile("sample.go", source, 0o600))
	require.NoError(t, os.WriteFile("sample_test.go", []byte("package sample\n"), 0o600))
	run("add", ".")
	run("commit", "-qm", "first")

	// One line of one function changed and two of another - one hunk, of two lines - a test file
	// changed, and a file that is new.
	lines := strings.Split(string(source), "\n")
	lines[11] = "\tp.marks = p.marks + 2"
	lines[32], lines[33] = "\t\treturn 99", "\t} // clamped"
	require.NoError(t, os.WriteFile("sample.go", []byte(strings.Join(lines, "\n")), 0o600))
	require.NoError(t, os.WriteFile("sample_test.go", []byte("package sample\n\n// changed\n"), 0o600))
	require.NoError(t, os.WriteFile("new.go", []byte("package sample\n\nfunc New() int { return 1 }\n"), 0o600))
	// A file that is not Go, changed in the same diff: its hunks are nobody's lines.
	require.NoError(t, os.WriteFile("notes.txt", []byte("a\nb\n"), 0o600))
	run("add", "notes.txt")

	changed, err := mutate.Changed("HEAD")
	require.NoError(t, err)
	assert.NotContains(t, changed, "", "a hunk of a file that is not Go was counted as a file with no name")

	assert.NotContains(t, changed, "sample_test.go", "a mutation of a test is a question about nothing")
	require.Contains(t, changed, "sample.go")
	assert.Equal(t, map[int]bool{12: true, 33: true, 34: true}, changed["sample.go"], "the lines the change touched are not the lines reported")
	require.Contains(t, changed, "new.go", "a file that is not committed yet is not in any diff, and is all new code")
	// Every line of it, counted as its newlines and the one after the last: nothing before it.
	assert.Equal(t, map[int]bool{1: true, 2: true, 3: true, 4: true}, changed["new.go"], "a new file is new from its first line to its last")
}

// A package with no tests had nobody to ask, and saying so is not the same as saying the tests
// let the edit through - counted together, a change to a command with no tests would read as a
// suite that refuses nothing.
func TestAPackageWithNoTestsIsSaidSoAndNotCountedAsASurvivor(t *testing.T) {
	// Not this package: running its own tests from inside them is a test that runs forever.
	outcome, _ := mutate.Run(mutate.Mutation{File: filepath.Join("..", "..", "cmd", "mutate", "main.go")}, mutate.Asked{Timeout: "1m"})
	assert.Equal(t, mutate.Untested, outcome, "a package with no test files was asked anyway")

	// And one that has tests is run: unmutated, they pass, which is what a survivor looks like.
	outcome, _ = mutate.Run(mutate.Mutation{File: filepath.Join("..", "fetch", "fetch.go")}, mutate.Asked{Timeout: "2m"})
	assert.Equal(t, mutate.Survived, outcome, "a package whose tests all pass was not run, or was misread")

	// A package with no tests of its own whose importer has some is asked through the importer,
	// not reported as untested: that is where its behaviour is held.
	importersRan := func() int {
		for _, c := range mutate.Costs() {
			if c.What == "alone: build and run the importers' tests" {
				return c.N
			}
		}
		return 0
	}
	before := importersRan()
	outcome, out := mutate.Run(mutate.Mutation{File: filepath.Join("..", "..", "cmd", "mutate", "main.go")},
		mutate.Asked{Timeout: "2m", Importers: []string{"../fetch"}})
	assert.Equal(t, mutate.Survived, outcome, "the importer's tests were not run: %s", out)
	assert.Contains(t, out, "internal/fetch", "the run does not say which package answered")
	assert.Equal(t, before+1, importersRan(), "the importers' run is not counted as theirs")
}

// A test that fails is an edit refused - the answer this gate exists for, and one no other test
// here reaches, since every package they run passes. The failing package is a module of its own in
// a temporary directory, so the refusal is a real `go test` failing and not a path go could not use.
func TestAnEditATestRefusesIsCountedRefused(t *testing.T) {
	dir := refuses(t)
	m := doubling(t, dir)
	overlay, err := mutate.Overlay(m, t.TempDir())
	require.NoError(t, err)
	outcome, out := mutate.Run(m, mutate.Asked{Overlay: overlay, Timeout: "2m"})
	assert.Equal(t, mutate.Killed, outcome, "a package whose test fails was not counted as refusing: %s", out)
	assert.Contains(t, out, "not doubled", "what refused it was not the package's own test")
}

// A package whose tests are all of another build has none to ask, as Schemata finds: an edit
// built alone is said untested, not counted as a survivor of tests that never ran.
func TestAPackageWhoseTestsAreOfAnotherBuildIsUntested(t *testing.T) {
	dir := refuses(t)
	test := filepath.Join(dir, "code_test.go")
	body, err := os.ReadFile(test)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(test, append([]byte("//go:build never\n\n"), body...), 0o600))
	m := doubling(t, dir)
	overlay, err := mutate.Overlay(m, t.TempDir())
	require.NoError(t, err)
	outcome, out := mutate.Run(m, mutate.Asked{Overlay: overlay, Timeout: "2m"})
	assert.Equal(t, mutate.Untested, outcome, "tests of another build were counted as asked: %s", out)
}

// refuses is a module of its own: Double, which TestDouble holds, and Half, which only TestHalf
// runs and nothing checks.
func refuses(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod": "module example.com/refuses\n\ngo 1.24\n",
		"code.go": "package refuses\n\nfunc Double(n int) int {\n\treturn n + n\n}\n\nfunc Half(n int) int {\n\treturn n / 2\n}\n\n" +
			"func Sign(n int) int {\n\tswitch {\n\tcase n < 0:\n\t\treturn -1\n\t}\n\treturn 1\n}\n",
		// TestHalf reads a file beside it, as tests here read their testdata: run from anywhere but
		// the package's directory, it never reaches Half.
		"code_test.go": "package refuses\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestDouble(t *testing.T) {\n\tif Double(2) != 4 {\n\t\tt.Fatal(\"not doubled\")\n\t}\n}\n\n" +
			"func TestHalf(t *testing.T) {\n\tif _, err := os.ReadFile(\"code.go\"); err != nil {\n\t\tt.Fatal(err)\n\t}\n\t_ = Half(4)\n}\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	return dir
}

// doubling is the edit that breaks Double: its + made a -.
func doubling(t *testing.T, dir string) mutate.Mutation {
	t.Helper()
	plan, err := mutate.Plan(filepath.Join(dir, "code.go"), map[int]bool{4: true})
	require.NoError(t, err)
	for _, m := range plan {
		if strings.Contains(m.What, "becomes") {
			return m
		}
	}
	t.Fatalf("no edit swaps Double's operator: %v", whats(plan))
	return mutate.Mutation{}
}

// The value returned beside an error that is never nil is read by nobody, so changing it is not
// a question; beside an error that may be nil, it is.
func TestAValueBesideAFailureIsNotBroken(t *testing.T) {
	source := `package p

func F(x int, y *T) (int, error) {
	n, err := g(x)
	if err != nil {
		return 10, err
	}
	switch {
	case x > 9:
		return 11, fmt.Errorf("too big")
	case x > 8:
		return 12, errors.New("big")
	case x > 7:
		return 13, connect.NewError(connect.CodeInvalidArgument, err)
	case x < 0:
		return 14, ErrNegative
	case x < -1:
		return 15, y.ErrGone
	case x < -2:
		return 16, errNoStore
	case x < -3:
		return 33, errs
	case x < -4:
		return 34, errored
	}
	if err == nil {
		return 20, err
	}
	if err != y {
		return 21, err
	}
	if f() != nil {
		return 22, err
	}
	if err != y.nothing {
		return 23, err
	}
	if other != nil {
		return 24, err
	}
	switch {
	case x > 1:
		return 25, g()
	case x > 2:
		return 26, other.New("may be nil")
	case x > 3:
		return 27, errors.Is(err, y)
	case x > 4:
		return 28, errs
	case x > 5:
		return 29, fmt.Sprint(x)
	case x > 6:
		return 32, connect.CodeOf(err)
	}
	return 30, err
}

func G() int {
	return 31
}
`
	path := filepath.Join(t.TempDir(), "fails.go")
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))
	lines := strings.Split(source, "\n")
	all := map[int]bool{}
	for i := range lines {
		all[i+1] = true
	}
	plan, err := mutate.Plan(path, all)
	require.NoError(t, err)
	broken := map[string]bool{}
	for _, m := range plan {
		if strings.HasPrefix(strings.TrimSpace(lines[m.Line-1]), "return ") {
			broken[strings.Fields(lines[m.Line-1])[1]] = true
		}
	}
	for value, why := range map[string]string{
		"10,": "beside an error found not nil",
		"11,": "beside a new error", "12,": "beside a new error", "13,": "beside a new error",
		"14,": "beside a sentinel", "15,": "beside a sentinel", "16,": "beside a package's own sentinel",
	} {
		assert.False(t, broken[value], "the value %s was broken", why)
	}
	for value, why := range map[string]string{
		"20,": "beside an error found nil", "21,": "beside one compared with something else",
		"22,": "under a check of something else", "23,": "under a check of something else",
		"24,": "under a check of another name",
		"25,": "beside a call that may return nil", "26,": "beside a call that may return nil",
		"27,": "beside a call that is no error", "28,": "beside a name that was not checked",
		"33,": "beside a name that only begins like err", "34,": "beside a name that only begins like err",
		"29,": "beside a call that is no error", "32,": "beside a call that is no error",
		"30,": "beside an error that may be nil", "31": "that is the only one",
	} {
		assert.True(t, broken[value], "the value %s was left alone", why)
	}
}

// A mutation is put to the tests that execute its line, and only those: the package's coverage
// says which, per test.
func TestAnEditIsPutToTheTestsThatReachIt(t *testing.T) {
	dir := refuses(t)
	c, err := mutate.Cover(dir, "2m", make(chan struct{}, 2))
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, []string{"TestDouble"}, c.Reaching(filepath.Join(dir, "code.go"), 4))
	assert.Equal(t, []string{"TestHalf"}, c.Reaching(filepath.Join(dir, "code.go"), 8))
	assert.Equal(t, []string{}, c.Reaching(filepath.Join(dir, "code.go"), 16), "a line nothing executes was reached")

	m := doubling(t, dir)
	overlay, err := mutate.Overlay(m, t.TempDir())
	require.NoError(t, err)
	// Asked of the one test that reaches it, it is refused; of a test that does not, it survives:
	// only the named test ran.
	outcome, out := mutate.Run(m, mutate.Asked{Overlay: overlay, Timeout: "2m", Reaching: []string{"TestDouble"}})
	assert.Equal(t, mutate.Killed, outcome, "the one test that reaches the edit was not run: %s", out)
	outcome, out = mutate.Run(m, mutate.Asked{Overlay: overlay, Timeout: "2m", Reaching: []string{"TestHalf"}})
	assert.Equal(t, mutate.Survived, outcome, "a test that was not named ran: %s", out)
	// An edit that does not compile asks nothing.
	broken := m
	broken.With = "@"
	overlay, err = mutate.Overlay(broken, t.TempDir())
	require.NoError(t, err)
	outcome, out = mutate.Run(broken, mutate.Asked{Overlay: overlay, Timeout: "2m"})
	assert.Equal(t, mutate.Unbuildable, outcome, "an edit that does not build was counted: %s", out)
	// And reached by none, it is not run at all.
	outcome, out = mutate.Run(m, mutate.Asked{Overlay: overlay, Timeout: "2m", Reaching: []string{}})
	assert.Equal(t, mutate.Survived, outcome)
	assert.Empty(t, out, "the package's tests ran for a line none of them reaches")
}

// Who imports a package is asked of the whole module, from wherever this runs: the tests here run
// in internal/mutate/, where `./...` alone would be this directory's packages and nothing else.
func TestImportersAreTheWholeModulesWhereverThisRuns(t *testing.T) {
	importers, err := mutate.Importers()
	require.NoError(t, err)
	assert.Contains(t, importers["versions"], "./versions/upstream",
		"versions/upstream, which bumps what versions reads, is not among its importers")
}

// A listing that fails says why, in go list's own words: in CI it once failed with nothing but
// an exit status. A `go` of the test's own stands in, which answers for the module and then fails
// the listing.
func TestAListingThatFailsSaysWhatGoListSaid(t *testing.T) {
	bin := t.TempDir()
	fake := "#!/bin/sh\nif [ \"$2\" = \"-m\" ]; then echo \"example.com/m $PWD\"; exit 0; fi\n" +
		"echo 'pattern ./...: the reason' >&2\nexit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte(fake), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := mutate.Importers()
	require.ErrorContains(t, err, "pattern ./...: the reason")
}

// Generated code is its generator's. A regenerated sqlc file is in every diff that changes a
// query, and an edit to it asks about a file nobody may edit - the query's own tests are where
// the change is held.
func TestGeneratedCodeIsNotBroken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queries.sql.go")
	require.NoError(t, os.WriteFile(path, []byte("// Code generated by sqlc. DO NOT EDIT.\n\npackage db\n\n"+
		"func Pick(a, b int) int {\n\tif a < b {\n\t\treturn a\n\t}\n\treturn b\n}\n"), 0o600))

	plan, err := mutate.Plan(path, map[int]bool{5: true, 6: true, 7: true})
	require.NoError(t, err)
	assert.Empty(t, plan, "a generated file was planned for mutation: %v", whats(plan))
}
