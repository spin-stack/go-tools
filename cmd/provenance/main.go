// Command provenance writes the SLSA v1 provenance predicate of the workflow run it is in
// (package provenance), from the environment GitHub sets and the repository's versions.yaml, for
// cosign attest-blob --type slsaprovenance1 --predicate to sign over the files a release
// publishes. Outside a workflow it refuses.
//
//	go tool provenance -version v20261013.1 -out _output/provenance.json [-builder .github/workflows/build-release.yml]
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/spin-stack/go-tools/provenance"
	"github.com/spin-stack/go-tools/versions"
)

func main() {
	os.Exit(run(os.Getenv, os.Args[1:], os.Stderr)) // mutate-exempt: the process's own arguments, past its name; run is what the tests drive
}

// run answers the exit status: 0 written, 2 not.
func run(env func(string) string, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("provenance", flag.ContinueOnError)
	fs.SetOutput(stderr)
	version := fs.String("version", "", "the release the run builds")
	out := fs.String("out", "", "where to write the predicate")
	pins := fs.String("versions", versions.File, "the repository's versions.yaml")
	builder := fs.String("builder", "", "the reusable workflow the run signs in (.github/workflows/<file>); the dispatched one when empty")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" {
		say(stderr, "provenance: no -out")
		return 2
	}
	v, err := versions.Load(*pins)
	if err != nil {
		say(stderr, "provenance:", err)
		return 2
	}
	p, err := provenance.Build(env, *version, v, nil)
	if err == nil && *builder != "" {
		p, err = p.BuiltBy(*builder)
	}
	if err != nil {
		say(stderr, err)
		return 2
	}
	b, err := p.JSON()
	if err != nil {
		say(stderr, "provenance:", err)
		return 2 // mutate-exempt: strings and maps of strings always marshal; the error is json's signature, never reached
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil { //nolint:gosec // a predicate, published beside its files
		say(stderr, "provenance:", err)
		return 2
	}
	return 0
}

// say writes one line. A line that cannot be written changes no exit status, which is the answer.
func say(w io.Writer, a ...any) { _, _ = fmt.Fprintln(w, a...) }
