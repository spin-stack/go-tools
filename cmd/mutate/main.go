// Command mutate breaks what a change touched and reports the edits no test refused.
//
// A repository runs it as a Go tool (`tool github.com/spin-stack/go-tools/cmd/mutate` in its
// go.mod, `go tool mutate`), from anywhere in its module. What its tests need besides the code -
// a database they share, say - is the environment's: spin runs it under hack/testpg, which starts
// one PostgreSQL for every test process and says where in a variable.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spin-stack/go-tools/internal/mutate"
)

func main() {
	base := flag.String("base", "origin/main", "what the diff is against")
	maxRuns := flag.Int("max", 12, "how many mutations to run, at most: each is a build and a package's tests")
	timeout := flag.String("timeout", "5m", "how long one package's tests may take")
	// Half the machine: each is a build and a test binary, and a database package's tests hold
	// connections of their own.
	jobs := flag.Int("j", max(runtime.NumCPU()/2, 1), "how many mutations run at once")
	staleOnly := flag.Bool("stale-exempts", false, "report every mutate-exempt reason in the tree that covers no edit this tool makes, and fail on one")
	flag.Parse()

	if *staleOnly {
		os.Exit(reportStale())
	}

	changed, err := mutate.Changed(*base)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(2)
	}
	if len(changed) == 0 {
		fmt.Printf("mutate: this change touches no Go file outside its tests; nothing to break.\n")
		return
	}

	var planned []mutate.Mutation
	files := make([]string, 0, len(changed))
	for file := range changed {
		files = append(files, file)
	}
	sort.Strings(files)
	for _, file := range files {
		if _, err := os.Stat(file); err != nil {
			continue // deleted by the change
		}
		of, err := mutate.Plan(file, changed[file])
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(2)
		}
		planned = append(planned, of...)
	}
	if len(planned) == 0 {
		fmt.Printf("mutate: nothing in the %d changed file(s) can be broken in a way that means anything.\n", len(files))
		return
	}

	// Spread over the changed functions rather than taking the first of each file: a cap that
	// always lands in the same function asks one question many times.
	chosen := mutate.Spread(planned, *maxRuns)
	for i := range chosen {
		chosen[i].ID = i + 1
	}
	fmt.Printf("mutate: %d mutation(s) planned across %d file(s); running %d\n", len(planned), len(files), len(chosen))

	importers, err := mutate.Importers()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(2)
	}

	mutate.Share(*jobs)
	started := time.Now()
	t, err := mutate.RunAll(chosen, importers, *timeout, *jobs, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(2)
	}
	reportCosts(time.Since(started))
	survived := t.Survived
	fmt.Printf("mutate: %d refused, %d survived, %d did not build, %d in packages with no tests\n",
		t.Killed, len(survived), t.Unbuildable, t.Untested)
	if len(t.UntestedPackages) > 0 {
		// Said rather than counted as a survivor: a package with no tests is a decision
		// somebody made, and this gate is about the tests that exist.
		packages := make([]string, 0, len(t.UntestedPackages))
		for p := range t.UntestedPackages {
			packages = append(packages, p)
		}
		sort.Strings(packages)
		fmt.Printf("  no tests in: %s\n", strings.Join(packages, ", "))
	}
	if len(survived) == 0 {
		fmt.Println("OK: every change this broke, a test refused.")
		return
	}
	fmt.Println("\nEach line above marked SURVIVED is code whose behaviour no test holds, in its package or in the ones importing it:")
	for _, m := range survived {
		fmt.Printf("  %s\n", m)
	}
	fmt.Println("Answer it with a test that refuses the edit, or with `mutate-exempt: <reason>` on the line " +
		"when the edit changes nothing a caller can see - or, for a function only a lane this run does not " +
		"run can reach (root, KVM, a cloud account), `mutate-lane: <TestName>` in its doc comment, naming " +
		"the lane's test that holds it.")
	os.Exit(1)
}

// reportCosts says where the run's time went, by what each process was for: the processes of
// one purpose run side by side, so their wall is summed and can pass the run's own.
func reportCosts(took time.Duration) {
	var cpu time.Duration
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	_, _ = fmt.Fprintln(tw, "\tprocesses\twall\tCPU\t")
	for _, c := range mutate.Costs() {
		cpu += c.CPU
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t\n", c.What, c.N, c.Wall.Round(time.Second), c.CPU.Round(time.Second))
	}
	fmt.Printf("mutate: %s, %s of CPU\n", took.Round(time.Second), cpu.Round(time.Second))
	_ = tw.Flush()

	// The tests the edits were put to, where most of a run's time goes: one that takes seconds
	// and reaches much of a change is asked once for each edit in it.
	tests := mutate.TestCosts()
	if len(tests) == 0 {
		return
	}
	fmt.Println("mutate: the tests asked the longest in all")
	tw = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, c := range tests[:min(len(tests), 10)] {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%d×\t%s\n", c.Package, c.Test, c.N, c.Took.Round(100*time.Millisecond))
	}
	_ = tw.Flush()
}

// reportStale lists every reason over nothing this tool breaks, in the tracked Go that is not a
// test, and answers the exit status: 0 when there is none.
func reportStale() int {
	out, err := exec.Command("git", "ls-files", "*.go", ":!:*_test.go").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mutate: listing the tree: %v\n", err)
		return 2
	}
	files := strings.Fields(string(out))
	stale := 0
	for _, file := range files {
		lines, err := mutate.StaleExempts(file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 2
		}
		for _, line := range lines {
			stale++
			fmt.Printf("  %s:%d\n", file, line)
		}
	}
	if stale > 0 {
		fmt.Printf("\n%d mutate-exempt reason(s) above cover no edit this tool makes: nothing would be broken on\n"+
			"their line or the one under it, so they explain nothing. Remove them.\n", stale)
		return 1
	}
	fmt.Printf("OK: every mutate-exempt reason in %d file(s) covers an edit this tool makes.\n", len(files))
	return 0
}
