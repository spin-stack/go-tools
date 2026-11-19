// Command planguard says whether an OpenTofu plan may be applied without a person reading it
// (internal/planguard): nothing, apply - creates and in-place updates alone - or person, with each
// change that took something away. An installation applying on a schedule applies what says apply
// and opens a pull request for what says person.
//
//	tofu plan -out=plan.bin && tofu show -json plan.bin > plan.json
//	go tool planguard [-replaceable aws_launch_template,...] plan.json
//
// The verdict is the first line on stdout, the changes held after it; in a workflow it is also
// verdict=<it> in $GITHUB_OUTPUT. The exit status is 0 for any verdict and 2 for no answer, so a
// person's plan is not a failed step: what follows it is the workflow's choice.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spin-stack/go-tools/internal/planguard"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) // mutate-exempt: the process's own arguments; run is what the tests drive
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("planguard", flag.ContinueOnError)
	fs.SetOutput(stderr)
	replaceable := fs.String("replaceable", "", "resource types that may be replaced unread, comma-separated")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		say(stderr, "planguard: usage: planguard [-replaceable type,...] <plan.json>")
		return 2
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		say(stderr, "planguard:", err)
		return 2
	}
	var types []string
	if *replaceable != "" {
		types = strings.Split(*replaceable, ",")
	}
	d, err := planguard.Decide(raw, types)
	if err != nil {
		say(stderr, err)
		return 2
	}
	say(stdout, d.Verdict)
	for _, h := range d.Held {
		say(stdout, "  "+h)
	}
	if path := os.Getenv("GITHUB_OUTPUT"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // the path the runner gives
		if err != nil {
			say(stderr, "planguard:", err)
			return 2
		}
		_, werr := fmt.Fprintf(f, "verdict=%s\n", d.Verdict)
		if err := f.Close(); werr != nil || err != nil {
			say(stderr, "planguard: $GITHUB_OUTPUT:", werr, err)
			return 2
		}
	}
	return 0
}

// say writes one line. A line that cannot be written changes no exit status, which is the answer.
func say(w io.Writer, a ...any) { _, _ = fmt.Fprintln(w, a...) }
