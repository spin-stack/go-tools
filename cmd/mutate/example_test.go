package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The example the site shows (site/src/content/docs/mutate.mdx) is this command run over
// testdata/example: a change whose tests execute every statement it adds, and one edit of it they let
// through; then the test that answers it, and nothing let through. The site reads the code and
// both outputs from testdata, so what it says mutate prints is what mutate printed here.
// UPDATE_GOLDEN=1 writes the outputs.
func TestTheExampleTheSiteShows(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the command and runs it over a module of its own")
	}
	example, err := filepath.Abs(filepath.Join("testdata", "example"))
	require.NoError(t, err)
	bin := filepath.Join(t.TempDir(), "mutate")
	build := exec.Command("go", "build", "-o", bin, ".")
	out, err := build.CombinedOutput()
	require.NoError(t, err, "%s", out)

	repo := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.name=example", "-c", "user.email=example@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = repo
		// The machine's own git configuration has no say in what the example prints.
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	put := func(from, to string) {
		raw, err := os.ReadFile(filepath.Join(example, from))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(repo, to), raw, 0o600))
	}
	git("init", "-q")
	put("go.mod", "go.mod")
	put("base/limiter.go", "limiter.go")
	git("add", ".")
	git("commit", "-qm", "the limiter")
	put("change/limiter.go", "limiter.go")
	put("change/limiter_test.go", "limiter_test.go")
	git("add", ".")
	git("commit", "-qm", "Allow")

	mutate := func(golden string, status int) {
		cmd := exec.Command(bin, "-base", "HEAD~1", "-j", "1")
		cmd.Dir = repo
		out, err := cmd.Output()
		got := 0
		if exit := (*exec.ExitError)(nil); errors.As(err, &exit) {
			got = exit.ExitCode()
		} else {
			require.NoError(t, err)
		}
		assert.Equal(t, status, got, "%s", out)
		said := abridged(string(out))
		path := filepath.Join(example, golden)
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
	out, err = cover.CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Contains(t, string(out), "coverage: 100.0% of statements")
	mutate("survived.txt", 1)
	put("answer/limiter_test.go", "limiter_test.go")
	mutate("answered.txt", 0)
}

// abridged is what a run says about the edits, without what differs from one run to the next:
// how it bounds the tests' memory, how its package was built, where the time went, and the order
// the edits finished in - each is said as it is answered, and two answered at once are said in
// either order. They are sorted, so a survivor comes first.
func abridged(out string) string {
	var head, edits, tail []string
	verdict := false
	for _, line := range strings.Split(out, "\n") {
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
