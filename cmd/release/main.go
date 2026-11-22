// Command release names a repository's next release as spin-stack names releases: v<YYYYMMDD>.<NN>,
// the day in UTC and which release of that day (internal/release has the rule).
//
//	release <commit>            the next name today; refuses a commit a release already names
//	release -outputs <commit>   what a workflow makes of the commit, as lines for $GITHUB_OUTPUT
//
// The first is what `task release` tags, in every repository a person releases. The second never
// refuses: `version=<the release that names it>` and `new=false` when the commit is released
// already, the next name today and `new=true` otherwise, and `module=<its module version>`, the
// tag a go.mod reads - a workflow run again on a commit it released goes on to mend what the
// first run left undone.
//
// The tags are the clone's: whoever asks fetches them all first. A tag that is no release's name
// - brand's screens-v..., a module version - is not counted and names no commit.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spin-stack/go-tools/internal/release"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, git, time.Now()); err != nil { // mutate-exempt: the process's own arguments; run is what the test drives
		fmt.Fprintln(os.Stderr, "release:", err) //nolint:errcheck // the exit status says it
		os.Exit(1)                               // mutate-exempt: any status but 0 fails the step; run is what the tests drive
	}
}

// gitFunc is a git command's output, one line a word: the clone's, and a test's own.
type gitFunc func(args ...string) ([]string, error)

func run(args []string, w io.Writer, g gitFunc, now time.Time) error {
	fs := flag.NewFlagSet("release", flag.ContinueOnError)
	outputs := fs.Bool("outputs", false, "version=, new= and module= for $GITHUB_OUTPUT, of a commit released already too")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("release [-outputs] <commit>")
	}
	commit := fs.Arg(0)
	at, err := g("tag", "--points-at", commit)
	if err != nil {
		return err
	}
	v, isNew := release.Of(at), false
	if v == "" {
		all, err := g("tag", "--list")
		if err != nil {
			return err
		}
		v, isNew = release.Next(all, now), true
	}
	if !*outputs {
		if !isNew {
			return fmt.Errorf("%s is released already, as %s", commit, v)
		}
		_, err = fmt.Fprintln(w, v)
		return err
	}
	module, err := release.Module(v)
	if err != nil { // mutate-exempt: Of and Next answer only names of the form Module reads
		return err
	}
	_, err = fmt.Fprintf(w, "version=%s\nnew=%t\nmodule=%s\n", v, isNew, module)
	return err
}

func git(args ...string) ([]string, error) {
	out, err := exec.Command("git", args...).Output() //nolint:gosec // git, with the commit release.yml names: a tag listing, not a command line
	if err != nil {
		var said []byte
		if exit := (*exec.ExitError)(nil); errors.As(err, &exit) {
			said = exit.Stderr
		}
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, said)
	}
	return strings.Fields(string(out)), nil
}
