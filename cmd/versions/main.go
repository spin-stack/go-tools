// Command versions reads and moves what versions.yaml pins: it hands the Dockerfiles their
// build arguments, tells the Taskfiles a version or an image's reference, says what is behind,
// and rewrites one entry for a bump.
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
		os.Exit(1)
	}
}

const usage = `versions - what versions.yaml pins

Usage:
  versions args    <name>...          the --build-arg flags a Dockerfile takes the entries as
  versions env     <name>...          the same as KEY=value lines, for a shell to read
  versions version <name>             one entry's version
  versions ref     <name>             an image's reference, by its digest
  versions check                      what is behind its upstream; exits 1 when anything is
  versions bump    <name> [version]   pin an entry at a version, the newest by default

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
	case "version", "ref":
		return one(v, cmd, names)
	case "check":
		if len(names) != 0 {
			return errors.New("check: takes no names; it checks every entry")
		}
		return check(ctx, v)
	case "bump":
		return bump(ctx, v, *file, names)
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// one says one entry's version, or an image's reference.
func one(v *versions.Versions, cmd string, names []string) error {
	if len(names) != 1 {
		return fmt.Errorf("%s: name one entry", cmd)
	}
	e, err := v.Get(names[0])
	if err != nil {
		return err
	}
	if cmd == "version" {
		fmt.Println(e.Version)
		return nil
	}
	if e.Kind != versions.Image {
		return fmt.Errorf("%s is a %s, not an image", e.Name, e.Kind)
	}
	fmt.Println(e.Ref())
	return nil
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
	e, out, err := upstream.Bump(ctx, v, was.Name, version)
	if err != nil {
		return err
	}
	if err := os.WriteFile(file, out, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", file, err)
	}
	fmt.Printf("%s: %s %s -> %s %s\n", e.Name, was.Version, was.Pin, e.Version, e.Pin)
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

func check(ctx context.Context, v *versions.Versions) error {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tPINNED\tNEWEST\t")
	behind := 0
	for _, st := range upstream.Check(ctx, v) {
		mark := ""
		if st.Behind {
			mark, behind = "behind", behind+1
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s %s\n", st.Entry.Name, st.Entry.Version, st.Newest, mark, st.Note)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if behind > 0 {
		return fmt.Errorf("%d behind; versions bump <name> moves one", behind)
	}
	return nil
}
