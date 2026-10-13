// Command trustroot compares the pinned Sigstore trusted root (package trustroot) with the one
// TUF serves today, or writes today's over it. -check fails on any difference - a new Rekor log,
// a Fulcio certificate authority, a timestamp authority, a key retired - so a release built with a
// root Sigstore has moved past is one that says so rather than one whose machines refuse what
// Sigstore signs next. It asks Sigstore's TUF repository: what runs it needs the network.
//
//	go tool trustroot -check
//	go run ./cmd/trustroot -write    (in go-tools, to renew it)
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"

	"github.com/spin-stack/go-tools/trustroot"
)

func main() {
	os.Exit(run(fromTUF, os.Args[1:], os.Stdout, os.Stderr)) // mutate-exempt: the process's own arguments, past its name; run is what the tests drive
}

// fetcher is the trusted root TUF serves, as it serves it: Sigstore's repository in a build, the
// test's bytes in a test.
type fetcher func() ([]byte, error)

// fromTUF is today's trusted root, the bytes of the target TUF verified, not a re-encoding: the
// file is compared and written as Sigstore published it.
func fromTUF() ([]byte, error) {
	c, err := tuf.New(tuf.DefaultOptions())
	if err != nil { // mutate-exempt: Sigstore's repository, which no test reaches; lint's -check runs it against the real one
		return nil, err
	}
	return c.GetTarget("trusted_root.json")
}

// run answers the exit status: 0 the same, or written; 1 the pinned root is behind; 2 no answer.
func run(fetch fetcher, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("trustroot", flag.ContinueOnError)
	fs.SetOutput(stderr)
	check := fs.Bool("check", false, "fail when TUF's trusted root differs from the pinned one")
	write := fs.Bool("write", false, "write TUF's trusted root over "+trustroot.File)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *check == *write {
		say(stderr, "trustroot: say -check or -write")
		return 2
	}
	current, err := fetch()
	if err != nil {
		say(stderr, "trustroot: reading Sigstore's trusted root over TUF:", err)
		return 2
	}
	if _, err := root.NewTrustedRootFromJSON(current); err != nil {
		say(stderr, "trustroot: what TUF served is not a trusted root:", err)
		return 2
	}
	if *write {
		if err := os.WriteFile(trustroot.File, current, 0o644); err != nil { //nolint:gosec // a file committed to this repository
			say(stderr, "trustroot:", err)
			return 2
		}
		say(stdout, "trustroot: wrote", trustroot.File)
		return 0
	}
	if !bytes.Equal(current, trustroot.JSON()) {
		say(stdout, "trustroot: Sigstore's trusted root has moved past the pinned one: renew it in go-tools (go run ./cmd/trustroot -write) and take that go-tools")
		return 1
	}
	say(stdout, "trustroot: OK - the pinned trusted root is the one TUF serves")
	return 0
}

// say writes one line. A line that cannot be written changes no exit status, which is the answer.
func say(w io.Writer, a ...any) { _, _ = fmt.Fprintln(w, a...) }
