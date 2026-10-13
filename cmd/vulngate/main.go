// Command vulngate runs govulncheck over the packages it is given and fails on a vulnerability
// the code calls and that has a fix (internal/vulngate). It runs the scan itself, so the gate's
// exit status is its own and no pipe in a Taskfile can eat it. The scan asks vuln.go.dev for the
// database: a lint that runs it needs the network.
//
//	go tool vulngate [govulncheck flags] ./...
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"golang.org/x/vuln/scan"

	"github.com/spin-stack/go-tools/internal/vulngate"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var out bytes.Buffer
	cmd := scan.Command(ctx, append([]string{"-format", "json"}, args...)...)
	cmd.Stdout, cmd.Stderr = &out, stderr
	if err := cmd.Start(); err != nil {
		say(stderr, "vulngate:", err)
		return 2
	}
	// In json mode govulncheck exits 0 whatever it found; an error here is one it could not scan.
	if err := cmd.Wait(); err != nil {
		say(stderr, "vulngate: govulncheck:", err)
		return 2
	}
	findings, err := vulngate.Read(&out)
	if err != nil {
		say(stderr, err)
		return 2
	}
	return report(findings, stdout)
}

// report says what was found and answers the exit status: 1 when anything fails.
func report(findings []vulngate.Finding, w io.Writer) int {
	failing, listed := vulngate.Judge(findings)
	for _, f := range listed {
		say(w, "vulngate: not called, or not fixed:", f)
	}
	for _, f := range failing {
		say(w, "vulngate: called and fixed - upgrade:", f)
	}
	if len(failing) > 0 {
		return 1
	}
	say(w, "vulngate: OK - nothing the code calls has a fix it lacks")
	return 0
}

// say writes one line of the report. A line that cannot be written changes no exit status, which
// is the gate's answer.
func say(w io.Writer, a ...any) { _, _ = fmt.Fprintln(w, a...) }
