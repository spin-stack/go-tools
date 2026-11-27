// Command mutate breaks what a change touched and reports the edits no test refused.
//
// A repository runs it as a Go tool (`tool github.com/spin-stack/go-tools/cmd/mutate` in its
// go.mod, `go tool mutate`), from anywhere in its module. What its tests need besides the code -
// a database they share, say - is the environment's: spin runs it under hack/testpg, which starts
// one PostgreSQL for every test process and says where in a variable. Run in a cgroup systemd
// delegated it (`systemd-run --user --scope -p Delegate=yes`), it starts each test process in a
// child the kernel holds to its memory and its tasks.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spin-stack/go-tools/internal/interrupt"
	"github.com/spin-stack/go-tools/internal/mutate"
)

func main() {
	os.Exit(run(interrupt.Context(), os.Args[1:], os.Stdout, os.Stderr)) // mutate-exempt: the process's own arguments; run is what the tests drive
}

// run is the command, answering its exit status: 0 when no edit survived, 1 when one did, 2 when
// the edits could not be asked, and 130 when an interrupt or SIGTERM stopped it: what a shell
// answers for a process Ctrl+C ended, so a script tells a run stopped from one that failed.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mutate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", "origin/main", "what the diff is against")
	maxRuns := fs.Int("max", 12, "how many mutations to run, at most: each is a build and a package's tests")
	timeout := fs.String("timeout", "5m", "how long one package's tests may take at most: a test or a run is stopped at four times what it took unbroken, plus 10s, where sooner")
	// Half the machine: each is a build and a test binary, and a database package's tests hold
	// connections of their own.
	jobs := fs.Int("j", max(runtime.NumCPU()/2, 1), "how many mutations run at once") // mutate-exempt: how many run at once, not what they say
	// A test binary's memory is bounded by nothing else: -timeout is time, and an edit that makes
	// a loop append for ever has the machine long before it runs out.
	mem := fs.Float64("mem", 0, "how many GiB one test process and what it starts may hold, 0 for its share of the memory free when the run starts, negative for no bound (Linux)") // mutate-exempt: how much a test may hold, not what it says
	staleOnly := fs.Bool("stale-exempts", false, "report every mutate-exempt reason in the tree that covers no edit this tool makes, and fail on one")
	// Off unless named: a run from scratch is the one that can be trusted with a file a test
	// reads from outside its package (mutate.Cache says why).
	cacheDir := fs.String("cache", "", "a directory keeping the edits refused, except those stopped for their time, so a run over the same code asks only about the rest; empty for none")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *staleOnly {
		return reportStale(ctx, stdout, stderr)
	}
	failed := func(err error) int {
		_, _ = fmt.Fprintln(stderr, err)
		if ctx.Err() != nil {
			return 130
		}
		return 2
	}

	changed, err := mutate.Changed(ctx, *base)
	if err != nil {
		return failed(err)
	}
	if len(changed) == 0 {
		_, _ = fmt.Fprintf(stdout, "mutate: this change touches no Go file outside its tests; nothing to break.\n")
		return 0
	}

	planned, files, err := plan(changed)
	if err != nil {
		return failed(err)
	}
	if len(planned) == 0 {
		_, _ = fmt.Fprintf(stdout, "mutate: nothing in the %d changed file(s) can be broken in a way that means anything.\n", len(files))
		return 0
	}

	// Spread over the changed functions rather than taking the first of each file: a cap that
	// always lands in the same function asks one question many times.
	chosen := mutate.Spread(planned, *maxRuns)
	for i := range chosen {
		chosen[i].ID = i + 1 // mutate-exempt: an edit's ID only tells it from the others, and is never said
	}
	_, _ = fmt.Fprintf(stdout, "mutate: %d mutation(s) planned across %d file(s); running %d\n", len(planned), len(files), len(chosen))

	importers, err := mutate.Importers(ctx)
	if err != nil {
		return failed(err)
	}

	cache, chosen, known, err := fromCache(ctx, *cacheDir, chosen, importers, stdout)
	if err != nil {
		return failed(err)
	}

	mutate.Share(*jobs, int64(*mem*(1<<30))) // mutate-exempt: how much a test may hold, not what it says
	if err := confine(ctx, stdout); err != nil {
		return failed(err)
	}
	started := time.Now()
	t, err := mutate.RunAll(ctx, chosen, importers, *timeout, *jobs, stdout)
	if err != nil {
		return failed(err)
	}
	if cache != nil {
		if err := cache.Keep(t.Refused); err != nil {
			return failed(err)
		}
		t.Killed += len(known)
	}
	reportCosts(stdout, time.Since(started))
	survived := t.Survived
	_, _ = fmt.Fprintf(stdout, "mutate: %d refused, %d survived, %d did not build, %d in packages with no tests\n",
		t.Killed, len(survived), t.Unbuildable, t.Untested)
	if len(t.UntestedPackages) > 0 {
		// Said rather than counted as a survivor: a package with no tests is a decision
		// somebody made, and this gate is about the tests that exist.
		packages := make([]string, 0, len(t.UntestedPackages))
		for p := range t.UntestedPackages {
			packages = append(packages, p)
		}
		sort.Strings(packages)
		_, _ = fmt.Fprintf(stdout, "  no tests in: %s\n", strings.Join(packages, ", "))
	}
	if len(survived) == 0 {
		_, _ = fmt.Fprintln(stdout, "OK: every change this broke, a test refused.")
		return 0
	}
	_, _ = fmt.Fprintln(stdout, "\nEach line above marked SURVIVED is code whose behaviour no test holds, in its package or in the ones importing it:")
	for _, m := range survived {
		_, _ = fmt.Fprintf(stdout, "  %s\n", m)
	}
	_, _ = fmt.Fprintln(stdout, "Answer it with a test that refuses the edit, or with `mutate-exempt: <reason>` on the line "+
		"when the edit changes nothing a caller can see - or, for a function only a lane this run does not "+
		"run can reach (root, KVM, a cloud account), `mutate-lane: <TestName>` in its doc comment, naming "+
		"the lane's test that holds it.")
	return 1
}

// confine holds each test process in a cgroup of its own where this run was delegated one, and
// says which it does either way: a run that should have been given a cgroup and was not reads the
// same as one that was, until a test forks without end.
func confine(ctx context.Context, stdout io.Writer) error {
	dir, err := mutate.Confine(ctx)
	switch {
	case err != nil:
		return err
	case dir != "":
		_, _ = fmt.Fprintf(stdout, "mutate: each test process in a cgroup of its own under %s\n", dir)
	default:
		_, _ = fmt.Fprintf(stdout, "mutate: test memory measured from /proc; run under `systemd-run --user --scope -p Delegate=yes` to have the kernel hold it, and the processes a test starts\n")
	}
	return nil
}

// fromCache splits chosen into the edits to ask about and those the cache in dir says a run over
// the same code refused; with no dir, there is no cache and every edit is asked.
func fromCache(ctx context.Context, dir string, chosen []mutate.Mutation, importers map[string][]string, stdout io.Writer) (*mutate.Cache, []mutate.Mutation, []mutate.Mutation, error) {
	if dir == "" {
		return nil, chosen, nil, nil
	}
	cache, err := mutate.OpenCache(dir)
	if err != nil {
		return nil, nil, nil, err
	}
	fresh, known, err := cache.Known(ctx, chosen, importers)
	if err != nil {
		return nil, nil, nil, err
	}
	_, _ = fmt.Fprintf(stdout, "mutate: %d refused by a run over the same code already (%s); asking about %d\n", len(known), dir, len(fresh))
	return cache, fresh, known, nil
}

// reportCosts says where the run's time went, by what each process was for: the processes of
// one purpose run side by side, so their wall is summed and can pass the run's own.
func reportCosts(stdout io.Writer, took time.Duration) {
	var cpu time.Duration
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', tabwriter.AlignRight) // mutate-exempt: how the columns are spaced, not what they say
	_, _ = fmt.Fprintln(tw, "\tprocesses\twall\tCPU\t")
	for _, c := range mutate.Costs() {
		cpu += c.CPU
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t\n", c.What, c.N, c.Wall.Round(time.Second), c.CPU.Round(time.Second))
	}
	_, _ = fmt.Fprintf(stdout, "mutate: %s, %s of CPU\n", took.Round(time.Second), cpu.Round(time.Second))
	_ = tw.Flush()

	// The tests the edits were put to, where most of a run's time goes: one that takes seconds
	// and reaches much of a change is asked once for each edit in it.
	tests := mutate.TestCosts()
	if len(tests) == 0 {
		return
	}
	_, _ = fmt.Fprintln(stdout, "mutate: the tests asked the longest in all")
	tw = tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0) // mutate-exempt: how the columns are spaced, not what they say
	for _, c := range tests[:min(len(tests), 10)] {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%d×\t%s\n", c.Package, c.Test, c.N, c.Took.Round(100*time.Millisecond))
	}
	_ = tw.Flush()
}

// plan is every edit of the changed lines, and the changed files in order.
func plan(changed map[string]map[int]bool) ([]mutate.Mutation, []string, error) {
	var planned []mutate.Mutation
	files := slices.Sorted(maps.Keys(changed))
	for _, file := range files {
		if _, err := os.Stat(file); err != nil {
			continue // deleted by the change
		}
		of, err := mutate.Plan(file, changed[file])
		if err != nil {
			return nil, nil, err
		}
		planned = append(planned, of...)
	}
	return planned, files, nil
}

// reportStale lists every reason over nothing this tool breaks, in the tracked Go that is not a
// test, and answers the exit status: 0 when there is none.
func reportStale(ctx context.Context, stdout, stderr io.Writer) int {
	out, err := exec.CommandContext(ctx, "git", "ls-files", "*.go", ":!:*_test.go").Output()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mutate: listing the tree: %v\n", err)
		return 2
	}
	files := strings.Fields(string(out))
	stale := 0
	for _, file := range files {
		lines, err := mutate.StaleExempts(file)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 2
		}
		for _, line := range lines {
			stale++
			_, _ = fmt.Fprintf(stdout, "  %s:%d\n", file, line)
		}
	}
	if stale > 0 {
		_, _ = fmt.Fprintf(stdout, "\n%d mutate-exempt reason(s) above cover no edit this tool makes: nothing would be broken on\n"+
			"their line or the one under it, so they explain nothing. Remove them.\n", stale)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "OK: every mutate-exempt reason in %d file(s) covers an edit this tool makes.\n", len(files))
	return 0
}
