// Package gittest runs git for a test where it cannot reach a repository the test did not make.
//
// A test runs inside a checkout - go test's working directory is the package's - and git looks for
// its repository upward from where it runs. So a git the test meant for a repository it made, run
// one level off, is a git on the checkout every worktree and every agent commits from: twice a
// test in spin set its author there (`ana@corp.example`, 2026-10 and 2026-11-12), and commits
// carried it to GitHub. Once it was a mutant of the code under test that lost its -C; once a
// TMPDIR inside the checkout made the "not a repository" the test asked for the checkout's own
// config. A hook's GIT_DIR, inherited by a test a hook runs, points every git at the checkout too.
//
// Command is the way a test here runs git (internal/gitexec refuses another), and Fence is what a
// package whose own code runs git calls in its TestMain: both stop git's search at the checkout
// and at the temporary directory, drop every GIT_ variable the test inherited, and leave out every
// configuration the machine has.
package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Identity is the author and committer of what a test commits through Command: an address at a
// reserved domain, which cmd/testidentity refuses on a commit of any repository.
const (
	IdentityName  = "gittest"
	IdentityEmail = "gittest@go-tools.invalid"
)

// Command is git with args in dir, which must be under the temporary directory: the repository
// is the one there, or none, never one above it. An option that names another repository (-C,
// --git-dir, --work-tree) is refused - it is the escape this exists to close. Env holds the
// fence and Identity; a caller appends to it to set more, and the last value of a key wins.
func Command(t *testing.T, dir string, args ...string) *exec.Cmd {
	t.Helper()
	if why := refused(dir, args); why != "" {
		t.Fatal("gittest: " + why)
	}
	cmd := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // a test's own arguments
	cmd.Dir = dir
	cmd.Env = append(append(withoutGit(os.Environ()), fence()...),
		"GIT_AUTHOR_NAME="+IdentityName, "GIT_AUTHOR_EMAIL="+IdentityEmail,
		"GIT_COMMITTER_NAME="+IdentityName, "GIT_COMMITTER_EMAIL="+IdentityEmail)
	return cmd
}

// refused says why git with args in dir could reach a repository the test did not make, or ""
// when it cannot.
func refused(dir string, args []string) string {
	tmp := resolved(os.TempDir())
	if d := resolved(dir); d != tmp && !strings.HasPrefix(d, tmp+string(filepath.Separator)) {
		return "git runs under the temporary directory " + tmp + ", not in " + dir
	}
	for _, a := range args {
		if a == "-C" || strings.HasPrefix(a, "--git-dir") || strings.HasPrefix(a, "--work-tree") {
			return a + " names a repository other than " + dir + "'s; run the command in that directory"
		}
	}
	return ""
}

// Run is Command run, failing the test with git's words when git fails; it answers git's
// output, trimmed.
func Run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := Command(t, dir, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Fence puts the fence in this process's environment, for every git the code under test starts:
// called first in the TestMain of a package whose own code runs git (internal/gitexec holds
// it). A git there that lost its -C then finds no repository, rather than the checkout's.
func Fence() {
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "GIT_") {
			_ = os.Unsetenv(k) //nolint:errcheck // fails only on an empty or malformed name, which Environ does not give
		}
	}
	for _, kv := range fence() {
		k, v, _ := strings.Cut(kv, "=")
		_ = os.Setenv(k, v) //nolint:errcheck // fails only on an empty or malformed name, and these are constants
	}
}

// withoutGit is env with no GIT_ variable: a hook's GIT_DIR or GIT_INDEX_FILE points every git
// at the checkout.
func withoutGit(env []string) []string {
	return slices.DeleteFunc(env, func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return strings.HasPrefix(k, "GIT_")
	})
}

// fence is git's search stopped short of the checkout and of the temporary directory, with no
// global or system configuration read.
func fence() []string {
	ceilings := []string{resolved(os.TempDir())}
	if top := checkout(); top != "" {
		ceilings = append(ceilings, top)
	}
	return []string{
		"GIT_CEILING_DIRECTORIES=" + strings.Join(ceilings, string(filepath.ListSeparator)),
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
	}
}

// checkout is the nearest directory at or above the working directory that holds a .git - a
// directory, or a worktree's file - or "" when there is none.
func checkout() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for dir = resolved(dir); ; {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// resolved is path absolute and with its links followed, as git compares ceilings: a TMPDIR
// behind a link is otherwise a ceiling git never meets.
func resolved(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	return path
}
