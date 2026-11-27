package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/internal/gittest"
)

// The example the site shows (site/src/content/docs/mutate.mdx) is this command run over
// testdata/example: a change whose tests execute every statement it adds, and one edit of it they let
// through; then the test that answers it, and nothing let through. The site reads the code and
// both outputs from testdata, so what it says mutate prints is what mutate printed here.
// UPDATE_GOLDEN=1 writes the outputs.
func TestTheExampleTheSiteShows(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the tests of a module of its own, once for each edit")
	}
	// Absolute: the run is made from inside the repository.
	goldens, err := filepath.Abs(filepath.Join("testdata", "example"))
	require.NoError(t, err)
	repo, put := example(t)
	mutate := func(golden string, status int) {
		got, out, _ := mutateIn(t, repo, "-base", "HEAD~1", "-j", "1")
		assert.Equal(t, status, got, "%s", out)
		assert.Regexp(t, "(?m)^mutate: (each test process in a cgroup of its own under /|test memory measured from /proc; )", out,
			"a run says how it holds the tests' memory")
		assert.Contains(t, out, "\nmutate: the tests asked the longest in all\n", "a run says where its time went")
		said := abridged(out)
		path := filepath.Join(goldens, golden)
		if os.Getenv("UPDATE_GOLDEN") != "" {
			require.NoError(t, os.WriteFile(path, []byte(said), 0o600))
		}
		want, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, string(want), said)
	}
	// What the page opens with: the tests execute every statement, and still let an edit through.
	cover := exec.Command("go", "test", "-cover", "./...")
	cover.Dir = repo
	out, err := cover.CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Contains(t, string(out), "coverage: 100.0% of statements")
	mutate("survived.txt", 1)
	put("answer/limiter_test.go", "limiter_test.go")
	mutate("answered.txt", 0)
}

// A gate run again over the same code asks its tests only about the edits the cache does not
// hold as refused.
func TestACacheAsksOnlyWhatARunOverTheSameCodeDidNot(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the tests of a module of its own, once for each edit")
	}
	repo, put := example(t)
	put("answer/limiter_test.go", "limiter_test.go")
	cache := t.TempDir()
	args := []string{"-base", "HEAD~1", "-j", "1", "-cache", cache}

	status, out, _ := mutateIn(t, repo, args...)
	assert.Equal(t, 0, status, "%s", out)
	assert.Contains(t, out, fmt.Sprintf("mutate: 0 refused by a run over the same code already (%s); asking about 2\n", cache))
	assert.Contains(t, out, "mutate: 2 refused, 0 survived")

	status, out, _ = mutateIn(t, repo, args...)
	assert.Equal(t, 0, status, "%s", out)
	assert.Contains(t, out, fmt.Sprintf("mutate: 2 refused by a run over the same code already (%s); asking about 0\n", cache))
	assert.Contains(t, out, "mutate: 2 refused, 0 survived")
	assert.NotContains(t, out, "  refused   ", "an edit the cache holds is asked again")
}

// A change with nothing to break passes, and says why, rather than reading as a run that asked.
func TestAChangeWithNothingToBreakPasses(t *testing.T) {
	repo, _ := example(t)
	status, out, _ := mutateIn(t, repo, "-base", "HEAD")
	assert.Equal(t, 0, status, "%s", out)
	assert.Equal(t, "mutate: this change touches no Go file outside its tests; nothing to break.\n", out)

	limiter := filepath.Join(repo, "limiter.go")
	raw, err := os.ReadFile(limiter)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(limiter, append(raw, "\n// Nothing here runs.\n"...), 0o600))
	status, out, _ = mutateIn(t, repo, "-base", "HEAD")
	assert.Equal(t, 0, status, "%s", out)
	assert.Equal(t, "mutate: nothing in the 1 changed file(s) can be broken in a way that means anything.\n", out)
}

// An edit in a package with no tests is said, and does not fail the run: a package with no tests
// is a decision somebody made.
func TestAPackageWithNoTestsIsNamedAndDoesNotFail(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the tests of a module of its own")
	}
	repo, _ := example(t)
	require.NoError(t, os.Mkdir(filepath.Join(repo, "untested"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "untested", "less.go"),
		[]byte("package untested\n\nfunc Less(a, b int) bool { return a < b }\n"), 0o600))
	status, out, _ := mutateIn(t, repo, "-base", "HEAD")
	assert.Equal(t, 0, status, "%s", out)
	assert.Regexp(t, `mutate: 0 refused, 0 survived, 0 did not build, \d+ in packages with no tests\n  no tests in: untested\n`, out)
}

// With no -max, a change of more edits than twelve has twelve of them asked; and a run stopped
// once it has said so answers 130, what a shell answers for a process Ctrl+C ended.
func TestARunAsksTwelveEditsByDefaultAndAnInterruptStopsIt(t *testing.T) {
	repo := t.TempDir()
	gittest.Run(t, repo, "init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/many\n\ngo 1.24\n"), 0o600))
	gittest.Run(t, repo, "add", ".")
	gittest.Run(t, repo, "commit", "-qm", "the module")
	var many strings.Builder
	many.WriteString("package many\n")
	for i := range 13 {
		fmt.Fprintf(&many, "\nfunc Less%d(a, b int) bool { return a < b }\n", i)
	}
	require.NoError(t, os.WriteFile(filepath.Join(repo, "many.go"), []byte(many.String()), 0o600))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stdout := &said{on: " planned across ", then: cancel}
	t.Chdir(repo)
	status := run(ctx, []string{"-base", "HEAD"}, stdout, &said{})
	assert.Equal(t, 130, status, "%s", stdout)
	assert.Regexp(t, `mutate: \d+ mutation\(s\) planned across 1 file\(s\); running 12\n`, stdout.String())
}

// Where nothing can be asked, the answer is 2, never 0: a gate that cannot run has not passed.
func TestARunThatCannotAskAnswersTwo(t *testing.T) {
	for _, args := range [][]string{
		{"-base", "HEAD"},
		{"-stale-exempts"},
		{"-no-such-flag"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			// No repository: the fence stops git's search at the temporary directory.
			status, out, errOut := mutateIn(t, t.TempDir(), args...)
			assert.Equal(t, 2, status, "%s%s", out, errOut)
			assert.NotEmpty(t, errOut, "a run that cannot ask says why")
		})
	}
}

// -stale-exempts fails on a reason over a line this tool never breaks, and passes the tree where
// every reason answers an edit.
func TestStaleExemptsFailsOnAReasonThatAnswersNoEdit(t *testing.T) {
	repo := t.TempDir()
	gittest.Run(t, repo, "init", "-q")
	write := func(name, source string) {
		require.NoError(t, os.WriteFile(filepath.Join(repo, name), []byte(source), 0o600))
		gittest.Run(t, repo, "add", name)
	}
	write("go.mod", "module example.com/exempt\n\ngo 1.24\n")
	write("less.go", "package exempt\n\nfunc less(a, b int) bool {\n\treturn a < b // mutate-exempt: held\n}\n")

	status, out, _ := mutateIn(t, repo, "-stale-exempts")
	assert.Equal(t, 0, status, "%s", out)
	assert.Equal(t, "OK: every mutate-exempt reason in 1 file(s) covers an edit this tool makes.\n", out)

	write("stale.go", "package exempt\n\n// mutate-exempt: over nothing\nvar answer = 42\n")
	status, out, _ = mutateIn(t, repo, "-stale-exempts")
	assert.Equal(t, 1, status, "%s", out)
	assert.True(t, strings.HasPrefix(out, "  stale.go:3\n\n1 mutate-exempt reason(s) above cover no edit"), "%s", out)

	write("broken.go", "package exempt\n\nfunc {\n")
	status, out, errOut := mutateIn(t, repo, "-stale-exempts")
	assert.Equal(t, 2, status, "a file that cannot be read is no answer: %s", out)
	assert.Contains(t, errOut, "broken.go")
}

// example is a repository of testdata/example's base and change, one commit each, and put, which
// writes one of the example's files over the repository's.
func example(t *testing.T) (repo string, put func(from, to string)) {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("testdata", "example"))
	require.NoError(t, err)
	repo = t.TempDir()
	put = func(from, to string) {
		raw, err := os.ReadFile(filepath.Join(dir, from))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(repo, to), raw, 0o600))
	}
	// The machine's own git configuration has no say in what the example prints.
	gittest.Run(t, repo, "init", "-q")
	put("go.mod", "go.mod")
	put("base/limiter.go", "limiter.go")
	gittest.Run(t, repo, "add", ".")
	gittest.Run(t, repo, "commit", "-qm", "the limiter")
	put("change/limiter.go", "limiter.go")
	put("change/limiter_test.go", "limiter_test.go")
	gittest.Run(t, repo, "add", ".")
	gittest.Run(t, repo, "commit", "-qm", "Allow")
	return repo, put
}

// mutateIn is the command run in dir with args, as a repository runs it from its root: its exit
// status and what it printed to stdout and to stderr.
func mutateIn(t *testing.T, dir string, args ...string) (status int, stdout, stderr string) {
	t.Helper()
	t.Chdir(dir)
	var out, errOut said
	status = run(t.Context(), args, &out, &errOut)
	return status, out.String(), errOut.String()
}

// said is what a run printed to one stream: the edits are said as they are answered, from more
// than one goroutine. When a write holds on, then is called once.
type said struct {
	mu   sync.Mutex
	b    strings.Builder
	on   string
	then func()
}

func (s *said) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.then != nil && strings.Contains(string(p), s.on) {
		s.then()
		s.then = nil
	}
	return s.b.Write(p)
}

func (s *said) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// abridged is what a run says about the edits, without what differs from one run to the next:
// how it bounds the tests' memory, how its package was built, where the time went, and the order
// the edits finished in - each is said as it is answered, and two answered at once are said in
// either order. They are sorted, so a survivor comes first.
func abridged(out string) string {
	var head, edits, tail []string
	verdict := false
	for line := range strings.SplitSeq(out, "\n") {
		switch {
		case verdict:
			tail = append(tail, line)
		case strings.Contains(line, " planned across "):
			head = append(head, line)
		case strings.HasPrefix(line, "  SURVIVED  "), strings.HasPrefix(line, "  refused   "):
			edits = append(edits, line)
		case strings.HasPrefix(line, "mutate: ") && strings.Contains(line, " refused, "):
			verdict = true
			tail = append(tail, line)
		}
	}
	slices.Sort(edits)
	return strings.Join(slices.Concat(head, edits, tail), "\n")
}
