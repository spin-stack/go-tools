// Command attestgate is package attestgate's check of the files a step is about to upload: it
// fails when one of them has no attestation bundle beside it. The workflows themselves are held
// by attestgate.Gate, from a test.
//
//	go tool attestgate files -require provenance -except checksums.txt dist
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spin-stack/go-tools/attestgate"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) // mutate-exempt: the process's own arguments, past its name; run is what the tests drive
}

// run answers the exit status: 0 every file attested, 1 one is not, 2 no answer.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "files" {
		say(stderr, "attestgate: usage: attestgate files -require provenance[,cdx] [-except pattern,...] <dir>")
		return 2
	}
	fs := flag.NewFlagSet("attestgate files", flag.ContinueOnError)
	fs.SetOutput(stderr)
	require := fs.String("require", "", "the attestations each file needs: provenance, cdx")
	except := fs.String("except", "", "patterns of files that need none, comma-separated")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 1 || *require == "" {
		say(stderr, "attestgate: files takes -require and one directory")
		return 2
	}
	var skip []string
	if *except != "" {
		skip = strings.Split(*except, ",")
	}
	missing, err := attestgate.Files(fs.Arg(0), strings.Split(*require, ","), skip)
	if err != nil {
		say(stderr, err)
		return 2
	}
	for _, m := range missing {
		say(stdout, "attestgate: missing", m)
	}
	if len(missing) > 0 {
		return 1
	}
	say(stdout, "attestgate: OK - every file in", fs.Arg(0), "is attested") // mutate-exempt: which words the line says; the exit status is the answer
	return 0
}

// say writes one line. A line that cannot be written changes no exit status, which is the answer.
func say(w io.Writer, a ...any) { _, _ = fmt.Fprintln(w, a...) }
