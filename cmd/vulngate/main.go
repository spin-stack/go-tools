// Command vulngate runs govulncheck over the packages it is given and fails on a vulnerability
// the code calls and that has a fix (internal/vulngate). It runs the scan itself, so the gate's
// exit status is its own and no pipe in a Taskfile can eat it. The scan asks vuln.go.dev for the
// database: a lint that runs it needs the network.
//
//	go tool vulngate [govulncheck flags, each -flag=value] ./...
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"

	"golang.org/x/vuln/scan"

	"github.com/spin-stack/go-tools/internal/vulngate"
)

// scanOne is the environment a vulngate runs one govulncheck under, as a process of its own.
const scanOne = "VULNGATE_SCAN"

func main() {
	if os.Getenv(scanOne) != "" { // mutate-exempt: the child's way in; govulncheck's own run, which TestEachScanIsAProcessOfItsOwn drives
		os.Exit(child(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(run(context.Background(), ownProcess, goList, os.Args[1:], os.Stdout, os.Stderr)) // mutate-exempt: the process's own arguments, past its name; run is what the tests drive
}

// scanner is govulncheck asked for json: a process of its own in a build, a script in a test.
type scanner func(ctx context.Context, args []string, stdout, stderr io.Writer) error

// ownProcess runs govulncheck as this binary again, so each run's memory goes when it does: run
// after run in one process, what one left was not given back before the next built its graph,
// and spin's ./... peaked at 7.9 GB where its largest program alone is 4.1 GB (2026-10-13).
func ownProcess(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	self, err := os.Executable()
	if err != nil { // mutate-exempt: Linux answers from /proc/self/exe, which a running process has
		return err
	}
	cmd := exec.CommandContext(ctx, self, args...)
	cmd.Env = append(os.Environ(), scanOne+"=1")
	cmd.Stdout, cmd.Stderr = stdout, stderr
	return cmd.Run()
}

// govulncheck is one scan, as json: in json mode it exits 0 whatever it found, so an error is a
// scan that could not be made.
func govulncheck(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	cmd := scan.Command(ctx, append([]string{"-format", "json"}, args...)...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil { // mutate-exempt: Start fails only on a Cmd already started, and this one never is
		return err
	}
	return cmd.Wait()
}

// child is the process ownProcess starts: one scan, its exit status 2 when it could not be made.
func child(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if err := govulncheck(ctx, args, stdout, stderr); err != nil {
		say(stderr, "vulngate:", err)
		return 2 // mutate-exempt: any status but 0 is the parent's error; which one, nothing reads
	}
	return 0 // mutate-exempt: a scan that runs needs vuln.go.dev, which no test here reaches; task lint's own vulngate is that run
}

// golist is `go list` with args, its output's lines: go itself in a build, a test's answer in a
// test.
type golist func(ctx context.Context, args ...string) ([]string, error)

func goList(ctx context.Context, args ...string) ([]string, error) {
	out, err := exec.CommandContext(ctx, "go", append([]string{"list"}, args...)...).Output() //nolint:gosec // go list of the patterns this tool's own caller named
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}
	return strings.Fields(string(out)), nil
}

// run answers the exit status: 0 clean, 1 something fails, 2 no scan to judge.
//
// Each program the patterns name is scanned on its own, and then whatever no program of them
// reaches: govulncheck builds one call graph of everything it is handed, and of spin's 266
// packages at once that was 11.5 GB where the largest program alone is 4.1 GB (measured
// 2026-10-13) - the union costs far more than its parts, and the OOM killer took it. What a
// program calls is in its own graph, so the findings are the same.
func run(ctx context.Context, scan scanner, list golist, args []string, stdout, stderr io.Writer) int {
	runs, err := programs(ctx, list, args)
	if err != nil {
		say(stderr, "vulngate:", err)
		return 2
	}
	var findings []vulngate.Finding
	for _, r := range runs {
		var out bytes.Buffer
		// In json mode govulncheck exits 0 whatever it found; an error here is one it could not scan.
		if err := scan(ctx, r, &out, stderr); err != nil {
			say(stderr, "vulngate: govulncheck:", err)
			return 2
		}
		f, err := vulngate.Read(&out)
		if err != nil {
			say(stderr, err)
			return 2
		}
		findings = append(findings, f...)
	}
	return report(findings, stdout)
}

// programs are the govulncheck runs args make: its flags (each -flag=value) with one main package
// a run, then with the packages the patterns name that no main of them imports. With no
// patterns, the one run govulncheck makes of its default.
func programs(ctx context.Context, list golist, args []string) ([][]string, error) {
	var flags, patterns, listFlags []string
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "-tags="):
			listFlags = append(listFlags, a)
			flags = append(flags, a)
		case strings.HasPrefix(a, "-"):
			flags = append(flags, a)
		default:
			patterns = append(patterns, a)
		}
	}
	if len(patterns) == 0 {
		return [][]string{args}, nil
	}
	named, err := list(ctx, slices.Concat(listFlags, []string{"-f", "{{.ImportPath}}"}, patterns)...)
	if err != nil {
		return nil, err
	}
	mains, err := list(ctx, slices.Concat(listFlags, []string{"-f", "{{if eq .Name \"main\"}}{{.ImportPath}}{{end}}"}, patterns)...)
	if err != nil {
		return nil, err
	}
	var runs [][]string
	reached := map[string]bool{}
	if len(mains) > 0 {
		deps, err := list(ctx, slices.Concat(listFlags, []string{"-deps", "-f", "{{.ImportPath}}"}, mains)...)
		if err != nil {
			return nil, err
		}
		for _, d := range deps {
			reached[d] = true
		}
	}
	for _, m := range mains {
		runs = append(runs, slices.Concat(flags, []string{m}))
	}
	var rest []string
	for _, p := range named {
		if !reached[p] {
			rest = append(rest, p)
		}
	}
	if len(rest) > 0 {
		runs = append(runs, slices.Concat(flags, rest))
	}
	return runs, nil
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
