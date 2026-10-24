// Command sfw runs pnpm through Socket Firewall Free, at the binary this module pins for the
// platform (versions.yaml beside this file), and is the gate that pnpm is the one package manager
// a repository uses and every install goes through the firewall:
//
//	go tool sfw pnpm install --frozen-lockfile   # the install, through the firewall
//	go tool sfw check [dir]                      # fails on npm, yarn, their lockfiles, and an install that skips it
//
// The binary is fetched the first time into the user's cache and checked against its pin on
// every run. Free needs no account; it refuses what Socket has confirmed as malware and warns of
// what it suspects.
package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"

	"github.com/spin-stack/go-tools/internal/sfw"
	"github.com/spin-stack/go-tools/versions"
)

//go:embed versions.yaml
var pins []byte

const usage = `sfw - package installs through Socket Firewall Free

Usage:
  sfw <package manager> <args>...   run it through the firewall: sfw pnpm install --frozen-lockfile
  sfw check [dir]                   fail on npm, npx, yarn, their lockfiles, a package.json not on pnpm, and a pnpm install not through it
`

func main() {
	args := os.Args[1:]
	switch {
	case len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help":
		fmt.Print(usage)
		if len(args) == 0 {
			os.Exit(2)
		}
	case args[0] == "check":
		os.Exit(check(args[1:]))
	default:
		os.Exit(run(args))
	}
}

func check(args []string) int {
	root := "."
	if len(args) > 0 {
		root = args[0]
	}
	findings, err := sfw.Check(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) > 0 {
		fmt.Printf("sfw: %d finding(s): pnpm is the package manager, and it installs through `go tool sfw`\n", len(findings))
		return 1
	}
	fmt.Println("OK: pnpm is the one package manager, and every install goes through Socket Firewall.")
	return 0
}

// run is the package manager's command through the firewall, and its exit status.
func run(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	v, err := versions.Parse(pins)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	e, err := sfw.Entry(v, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sfw: %v\n", err)
		return 2
	}
	bin, err := sfw.Binary(ctx, e, filepath.Join(cache, "spin-stack", "sfw"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	// The terminal's interrupt reaches the firewall and the package manager as it reaches this:
	// they are one process group. This waits for them to end rather than leaving them running.
	stop()
	signal.Ignore(os.Interrupt)
	cmd := exec.Command(bin, args...) //nolint:gosec // the pinned firewall, running what the caller asked
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if exit := (*exec.ExitError)(nil); errors.As(err, &exit) {
			return exit.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "sfw: %v\n", err)
		return 2
	}
	return 0
}
