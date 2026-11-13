package testidentity

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/spin-stack/go-tools/internal/allow"
)

// ErrFound is a run that found a test identity on a commit, or an allow line for nothing.
var ErrFound = errors.New("a test identity, or a stale exemption")

// Run checks the commits of the repository at root since base - every one in base..HEAD - against
// the test identities of its tree, less what the allow file at allowPath exempts (none when it is
// ""), and writes what it found to out. It answers ErrFound when the gate fails, and another
// error when it could not ask.
func Run(ctx context.Context, root, base, allowPath string, out io.Writer) error {
	allowed, err := allow.Read(allowPath)
	if err != nil {
		return err
	}
	if _, err := git(ctx, root, "rev-parse", "--verify", "--quiet", base+"^{commit}"); err != nil {
		return fmt.Errorf("%s is not a commit here: the branch's commits are the ones after it, so fetch it "+
			"(git fetch origin, or fetch-depth: 0 in a workflow's checkout)", base)
	}
	tracked, err := git(ctx, root, "ls-files", "-z")
	if err != nil {
		return err
	}
	fixtures, err := Fixtures(root, strings.FieldsFunc(tracked, func(r rune) bool { return r == 0 }))
	if err != nil {
		return err
	}
	// %ae and %ce, not %aE: what the commit says, before a .mailmap maps it to somebody.
	log, err := git(ctx, root, "log", "-z", "--format=%H%x1f%s%x1f%ae%x1f%ce", base+"..HEAD")
	if err != nil {
		return err
	}
	commits, err := parseLog(log)
	if err != nil {
		return err
	}

	found := Check(commits, fixtures, allowed)
	stale := Stale(allowed, fixtures)
	for _, f := range found {
		_, _ = fmt.Fprintln(out, f) //nolint:errcheck // the status is the gate's answer; out failing changes no status
	}
	for _, email := range stale {
		_, _ = fmt.Fprintf(out, "%s: %s is not a test identity any more: take the line out\n", allowPath, email) //nolint:errcheck // as above
	}
	if len(found) > 0 {
		_, _ = fmt.Fprintf(out, remedy, base) //nolint:errcheck // as above
	}
	if len(found) > 0 || len(stale) > 0 {
		return ErrFound
	}
	_, err = fmt.Fprintf(out, "OK: %d commit(s) since %s, none by a test identity (%d addresses in the tree's tests and fixtures).\n",
		len(commits), base, len(fixtures))
	return err
}

// remedy is what to do about a finding, in the order that stops the next one first.
const remedy = `
A test identity reached a commit: git took it from a configuration a test wrote, most likely
the checkout's own. Before anything else, ask git where its author comes from, in the checkout
or worktree the commit was made in:

    git config --show-origin --get-all user.email
    git config --show-origin --get-all user.name

and take out what a test put there (git config --local --unset user.email, and user.name),
so the next commit is not the same. Then give the branch's commits their real author again:

    git rebase --exec 'git commit --amend --no-edit --reset-author' %s

and push with --force-with-lease. A commit already on the default branch stays; .mailmap maps
its address.
`

// parseLog is the commits of git log's -z output in the format Run asks for.
func parseLog(log string) ([]Commit, error) {
	var commits []Commit
	for _, rec := range strings.FieldsFunc(log, func(r rune) bool { return r == 0 }) {
		f := strings.Split(strings.TrimPrefix(rec, "\n"), "\x1f")
		if len(f) != 4 {
			return nil, fmt.Errorf("git log answered %q", rec)
		}
		commits = append(commits, Commit{SHA: f[0], Subject: f[1], AuthorEmail: f[2], CommitterEmail: f[3]})
	}
	return commits, nil
}

func git(ctx context.Context, root string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...).Output() //nolint:gosec // git, with the base the caller names
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}
