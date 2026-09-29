// Command testquality runs the testquality analyzer against a repository's allowlist of the
// findings it keeps on purpose (-allow; none by default), which is a ratchet: an entry nothing
// matches fails too.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spin-stack/go-tools/internal/allow"
	"github.com/spin-stack/go-tools/internal/testquality"
)

func main() {
	allowPath := flag.String("allow", "", "file of allowed exemptions, one key per line with the reason")
	flag.Parse()

	patterns := flag.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}

	allowed, err := allow.Read(*allowPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "testquality: %v\n", err)
		os.Exit(2)
	}

	findings, err := testquality.Findings(patterns...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(2)
	}

	var unexplained []string
	used := map[string]bool{}
	for _, f := range findings {
		if allowed[f.Key()] {
			used[f.Key()] = true
			continue
		}
		unexplained = append(unexplained, fmt.Sprintf("%s:%d: %s", relative(f.Pos.Filename), f.Pos.Line, f.Message))
	}

	// Stale exemptions fail too, or the list stops shrinking.
	stale := allow.Stale(allowed, used)
	sort.Strings(unexplained)

	for _, u := range unexplained {
		fmt.Fprintln(os.Stderr, u)
	}
	for _, s := range stale {
		fmt.Fprintf(os.Stderr, "%s is in %s and is no longer a finding: remove the line\n", s, *allowPath)
	}
	if len(unexplained) > 0 || len(stale) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d test(s) that cannot fail or never run, %d stale exemption(s)\n",
			len(unexplained), len(stale))
		os.Exit(1)
	}
	fmt.Printf("testquality: %d exemption(s), no new findings\n", len(allowed))
}

func relative(path string) string {
	wd, err := os.Getwd()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(wd, path)
	if err != nil {
		return path
	}
	return rel
}
