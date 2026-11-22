// Command versions reads and moves what versions.yaml pins: it hands the Dockerfiles their
// build arguments, tells the Taskfiles a version or an image's reference, says what is behind,
// fetches a download as the bytes it pins, and rewrites one entry for a bump.
//
// A repository runs it as a Go tool (`tool github.com/spin-stack/go-tools/cmd/versions` in its
// go.mod, `go tool versions ...`): at build time, from a checkout, at the version its go.mod
// pins, and not linked into anything it ships.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spin-stack/go-tools/versions"
	"github.com/spin-stack/go-tools/versions/upstream"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctx, os.Args[1:])
	stop()
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		// The package names itself in its errors already; this names the rest.
		msg := err.Error()
		if !strings.HasPrefix(msg, "versions: ") {
			msg = "versions: " + msg
		}
		fmt.Fprintln(os.Stderr, msg)
		os.Exit(status(err))
	}
}

// unread is check's failure when it could not read a row: another exit status than a row behind,
// so that what a script reads as "nothing to bump" is never a file it was not shown.
type unread struct{ error }

// status is the exit status of a failure: 2 for rows check could not read, 1 for any other, a
// row behind among them - as diff answers 1 for a difference and 2 for a file it could not read.
func status(err error) int {
	if errors.As(err, &unread{}) {
		return 2
	}
	return 1
}

const usage = `versions - what versions.yaml pins

Usage:
  versions args    <name>...          the --build-arg flags a Dockerfile takes the entries as
  versions env     <name>...          the same as KEY=value lines, for a shell to read
  versions version <name>             one entry's version
  versions ref     <name>             an image's reference, by its digest
  versions url     <name>             a download's URL, at its version
  versions fetch   <name> <dir>       a download in dir, only as the bytes it pins, and its path
  versions newer   <name> <version>   <version> when it is newer than the pin, nothing when not
  versions check                      what is behind its upstream; exits 1 when anything is,
                                      and 2 when an entry could not be read, which may be
                                      behind
  versions bump    <name> [version]   pin an entry at a version, the newest by default, and
                                      every entry tracked with it at the same
  versions refresh <name>...          bump each to its newest, a commit each, and say what moved
                                      and what is a new major, left for a person;
                                      -propose pushes them as refresh/<label>-<sum> and opens
                                      the pull request (GH_TOKEN or GITHUB_TOKEN), once per set,
                                      closing the open one it supersedes (-label, -base)

Every command takes -file, versions.yaml by default. Nothing is built by a bump: each entry's
note says what it is checked with afterwards.
`

func run(ctx context.Context, argv []string) error {
	if len(argv) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("no command given")
	}
	cmd, argv := argv[0], argv[1:]
	if cmd == "-h" || cmd == "--help" || cmd == "help" {
		fmt.Print(usage)
		return nil
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	file := fs.String("file", versions.File, "the versions.yaml to read")
	propose := fs.Bool("propose", false, "refresh: open a pull request of what moved")
	label := fs.String("label", "versions", "refresh: the name of the branches it proposes")
	base := fs.String("base", "main", "refresh: the branch the pull request is into")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Print(usage)
		}
		return fmt.Errorf("%s: %w", cmd, err)
	}
	names := fs.Args()
	v, err := versions.Load(*file)
	if err != nil {
		return err
	}

	switch cmd {
	case "args", "env":
		if len(names) == 0 {
			return fmt.Errorf("%s: name at least one entry", cmd)
		}
		prefix, sep := "--build-arg ", " "
		if cmd == "env" {
			prefix, sep = "", "\n"
		}
		pins, err := pins(v, names, prefix)
		if err != nil {
			return err
		}
		fmt.Println(strings.Join(pins, sep))
		return nil
	case "version", "ref", "url":
		return one(os.Stdout, v, cmd, names)
	case "fetch":
		return fetch(ctx, os.Stdout, v, names)
	case "newer":
		return newer(os.Stdout, v, names)
	case "check":
		if len(names) != 0 {
			return errors.New("check: takes no names; it checks every entry")
		}
		return check(os.Stdout, upstream.Check(ctx, v))
	case "bump":
		return bump(ctx, v, *file, names)
	case "refresh":
		r := refresher{file: *file, git: runGit, label: *label, base: *base, out: os.Stdout, now: time.Now()}
		return refresh(ctx, r, *propose, names)
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// one says one entry's version, an image's reference, or a download's URL.
func one(w io.Writer, v *versions.Versions, cmd string, names []string) error {
	if len(names) != 1 {
		return fmt.Errorf("%s: name one entry", cmd)
	}
	e, err := v.Get(names[0])
	if err != nil {
		return err
	}
	var out string
	switch {
	case cmd == "version":
		out = e.Version
	case cmd == "url" && e.Kind == versions.Download:
		out = e.URL()
	case cmd == "ref" && e.Kind == versions.Image:
		out = e.Ref()
	default:
		return fmt.Errorf("%s: %s is a %s", cmd, e.Name, e.Kind)
	}
	_, err = fmt.Fprintln(w, out)
	return err
}

// fetch puts an entry's download in a directory, only as the bytes it pins, and prints its path.
func fetch(ctx context.Context, w io.Writer, v *versions.Versions, names []string) error {
	if len(names) != 2 {
		return errors.New("fetch: <name> <dir>")
	}
	e, err := v.Get(names[0])
	if err != nil {
		return err
	}
	file, err := upstream.PinnedFile(ctx, e, names[1])
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, file)
	return err
}

// newer prints the version given when it is newer than the entry's, and nothing when it is not:
// what a workflow told of a release asks before proposing a bump, so one told twice, or of an
// old release, proposes nothing (spin's machine.yml). A version not of the entry's form is an
// error, not an old version.
func newer(w io.Writer, v *versions.Versions, names []string) error {
	if len(names) != 2 {
		return errors.New("newer: <name> <version>")
	}
	e, err := v.Get(names[0])
	if err != nil {
		return err
	}
	is, err := e.Newer(names[1])
	if err != nil || !is {
		return err
	}
	_, err = fmt.Fprintln(w, names[1])
	return err
}

// bump pins an entry at a version, the newest by default, and writes the file.
func bump(ctx context.Context, v *versions.Versions, file string, names []string) error {
	if len(names) < 1 || len(names) > 2 {
		return errors.New("bump: <name> [version]")
	}
	version := ""
	if len(names) == 2 {
		version = names[1]
	}
	was, err := v.Get(names[0])
	if err != nil {
		return err
	}
	_, out, err := upstream.Bump(ctx, v, was.Name, version)
	if err != nil {
		return err
	}
	if err := os.WriteFile(file, out, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", file, err)
	}
	// The entry named and every entry with it: what a commit of the bump moves.
	bumped, err := versions.Parse(out)
	if err != nil { // mutate-exempt: out is what Bump read back whole
		return err
	}
	for i, e := range bumped.Entries {
		if old := v.Entries[i]; e.Name == was.Name || e.Version != old.Version || e.Pin != old.Pin {
			fmt.Printf("%s: %s %s -> %s %s\n", e.Name, old.Version, old.Pin, e.Version, e.Pin)
		}
	}
	return nil
}

// pins is each KEY=value the entries named are handed over as, after prefix, sorted: the same
// entries are the same command line, which is what a comparison of two of them relies on.
func pins(v *versions.Versions, names []string, prefix string) ([]string, error) {
	var out []string
	for _, n := range names {
		e, err := v.Get(n)
		if err != nil {
			return nil, err
		}
		for k, val := range e.Args() {
			out = append(out, prefix+k+"="+val)
		}
	}
	slices.Sort(out)
	return out, nil
}

// check prints where each entry stands and fails when one is behind, or could not be read. The
// two are counted apart and fail apart: a row not read - a private upstream asked without a token
// answers 404 - may be behind, so a count of what is behind says "of those read" beside it.
func check(w io.Writer, rows []upstream.Status) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tPINNED\tNEWEST\t")
	behind, unreadRows := 0, 0
	for _, st := range rows {
		mark := ""
		switch {
		case st.Unread:
			mark, unreadRows = "not read:", unreadRows+1
		case st.Behind:
			mark, behind = "behind", behind+1
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s %s\n", st.Entry.Name, st.Entry.Version, st.Newest, mark, st.Note)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	switch {
	case unreadRows > 0:
		return unread{fmt.Errorf("%d not read, and %d behind of the %d that were: a row not read may be behind, and says why "+
			"(GITHUB_TOKEN or GH_TOKEN reads a private repository, and more than sixty requests an hour)", unreadRows, behind, len(rows)-unreadRows)}
	case behind > 0:
		return fmt.Errorf("%d behind; versions bump <name> moves one", behind)
	}
	return nil
}
