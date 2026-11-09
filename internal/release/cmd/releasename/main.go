// Command releasename says what release.yml makes of a commit, as lines for $GITHUB_OUTPUT:
// `version=<the release that names it>` and `new=false` when it is released already, the next
// name today and `new=true` otherwise.
//
//	go run ./internal/release/cmd/releasename <commit>
//
// The tags are the clone's: release.yml fetches them all before asking.
package main

import (
	"errors"
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
		fmt.Fprintln(os.Stderr, "releasename:", err) //nolint:errcheck // the exit status says it
		os.Exit(1)                                   // mutate-exempt: any status but 0 fails the step; run is what the tests drive
	}
}

// gitFunc is a git command's output, one line a word: the clone's, and a test's own.
type gitFunc func(args ...string) ([]string, error)

func run(args []string, w io.Writer, g gitFunc, now time.Time) error {
	if len(args) != 1 {
		return errors.New("releasename <commit>")
	}
	at, err := g("tag", "--points-at", args[0])
	if err != nil {
		return err
	}
	if v := release.Of(at); v != "" {
		_, err := fmt.Fprintf(w, "version=%s\nnew=false\n", v)
		return err
	}
	all, err := g("tag", "--list")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "version=%s\nnew=true\n", release.Next(all, now))
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
