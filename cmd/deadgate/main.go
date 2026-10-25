// Command deadgate fails on a function a repository's binaries link but never call, and on a
// package none of them imports, unless one of two lists says why: the allowlist, for what is
// unreachable and correct for ever, and the pending list, for a deletion somebody has to finish.
// Both are ratchets: an entry matching no finding fails too.
//
//	go tool deadgate -allow file -pending file [-deadcode path] [-roots ./cmd/...] [-skip ./cmd/x,...] [-packages ./...]
//
// The roots are every main package under -roots but those -skip names, and only the binaries:
// `deadcode -test` would make every test's own subject reachable, answering "is this called by
// anything at all" rather than "does a binary reach it". The analysis is golang.org/x/tools'
// deadcode, which -deadcode names, built against the x/tools the module compiles with.
//
// Two blind spots make this a floor and not a proof: RTA marks every method of a type that
// reaches reflect as live, so absence from the report is not evidence of a caller; and a package
// no binary imports is not in the program at all - the package pass over -packages closes that
// gap at package granularity. What it says holds for one GOOS/GOARCH/build-tag configuration,
// the one it runs in.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spin-stack/go-tools/internal/allow"
	"github.com/spin-stack/go-tools/internal/deadgate"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) // mutate-exempt: the process's own arguments; run is what the test drives
}

// run answers the exit status: 0 every finding explained and every reason still true, 1 not,
// 2 no answer.
func run(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("deadgate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	allowPath := fs.String("allow", "", "unreachable, and correct for ever: `<symbol>  # why` a line (required)")
	pendingPath := fs.String("pending", "", "unreachable, and owed a deletion: the same form (required)")
	binary := fs.String("deadcode", "deadcode", "golang.org/x/tools/cmd/deadcode, built against the module's x/tools")
	roots := fs.String("roots", "./...", "the pattern every binary is under")
	skip := fs.String("skip", "", "binaries that are no root, comma-separated as ./cmd/x: their own packages are outside the gate")
	packages := fs.String("packages", "./...", "the pattern the package pass reads: each package there no root imports is a finding")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *allowPath == "" || *pendingPath == "" {
		_, _ = fmt.Fprintln(stderr, "deadgate: -allow and -pending are both read on every run; an empty list is a file with only comments")
		return 2
	}
	var skipped []string
	if *skip != "" {
		skipped = strings.Split(*skip, ",")
	}
	report, err := judge(*binary, *roots, *packages, skipped, *allowPath, *pendingPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "deadgate:", err)
		return 2
	}
	report.Write(stdout, *allowPath, *pendingPath)
	if !report.OK() {
		return 1
	}
	return 0
}

// judge is the report: the roots built, deadcode run over them, the package pass, and the two
// lists read.
func judge(binary, rootsPattern, packages string, skipped []string, allowPath, pendingPath string) (deadgate.Report, error) {
	var r deadgate.Report
	// The lists first: a list with an entry and no reason is refused before a minute of building.
	allowed, err := allow.Read(allowPath)
	if err != nil {
		return r, err
	}
	pending, err := allow.Read(pendingPath)
	if err != nil {
		return r, err
	}
	module, err := sh("go", "list", "-m")
	if err != nil {
		return r, err
	}
	module = strings.TrimSpace(module)
	mains, err := sh("go", "list", "-f", `{{if eq .Name "main"}}{{.ImportPath}}{{end}}`, rootsPattern)
	if err != nil {
		return r, err
	}
	roots := deadgate.Roots(strings.Fields(mains), module, skipped)
	if len(roots) == 0 {
		return r, fmt.Errorf("no main package under %s is a root", rootsPattern)
	}
	// deadcode answers about the program it managed to load, and it exits 0 having loaded a broken
	// one: a package with type errors is simply absent, so a tree that does not compile produces
	// a *shorter* report - the direction that hides findings. Into a directory: go build writes a
	// single root's binary where it stands otherwise.
	out, err := os.MkdirTemp("", "deadgate")
	if err != nil {
		return r, err
	}
	defer func() { _ = os.RemoveAll(out) }()
	if _, err := sh("go", append([]string{"build", "-o", out + "/"}, roots...)...); err != nil {
		return r, fmt.Errorf("the roots do not build, so deadcode would report on a program that is missing packages; a short report here would mean 'not analysed', not 'nothing dead': %w", err)
	}
	// -filter anchored: unanchored, a module's path also matches a module whose path begins with
	// it, and the report fills with unreachable methods of a dependency the repository does not own.
	report, err := sh(binary, append([]string{"-filter", "^" + module + "/", `-f={{range .Funcs}}{{$.Path}}.{{.Name}}{{"\n"}}{{end}}`}, roots...)...)
	if err != nil {
		return r, err
	}
	found := deadgate.Symbols(report, module, trimDots(skipped))
	all, err := sh("go", "list", packages)
	if err != nil {
		return r, err
	}
	deps, err := sh("go", append([]string{"list", "-deps"}, roots...)...)
	if err != nil {
		return r, err
	}
	found = append(found, deadgate.Unimported(strings.Fields(all), strings.Fields(deps), module, trimDots(skipped))...)
	return deadgate.Judge(found, allowed, pending), nil
}

// trimDots is the skipped binaries as module-relative paths, ./cmd/x as cmd/x.
func trimDots(skipped []string) []string {
	out := make([]string, len(skipped))
	for i, s := range skipped {
		out[i] = strings.TrimPrefix(s, "./")
	}
	return out
}

// sh is a command's standard output; what it said on standard error is in the error.
func sh(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output() //nolint:gosec // go and the deadcode binary the caller names, over its own module
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return string(out), nil
}
