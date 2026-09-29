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
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	chosen := spread(planned, *maxRuns)
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
	t := runAll(chosen, importers, *timeout, *jobs)
	reportCosts(time.Since(started))
	survived := t.survived
	fmt.Printf("mutate: %d refused, %d survived, %d did not build, %d in packages with no tests\n",
		t.killed, len(survived), t.unbuildable, t.untested)
	if len(t.untestedPackages) > 0 {
		// Said rather than counted as a survivor: a package with no tests is a decision
		// somebody made, and this gate is about the tests that exist.
		packages := make([]string, 0, len(t.untestedPackages))
		for p := range t.untestedPackages {
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

// tally is what the tests did about every mutation.
type tally struct {
	survived                      []mutate.Mutation
	killed, unbuildable, untested int
	untestedPackages              map[string]bool
}

// result is what the tests did about one mutation.
type result struct {
	m       mutate.Mutation
	outcome mutate.Outcome
}

// runAll puts every mutation to the tests, jobs at a time, and says each outcome as it comes.
// Each package's mutations are built into one binary (mutate.Schemata), run once per mutation;
// an edit the schema could not build is built alone, through an overlay of its own. Each
// package's coverage is measured once, for both.
func runAll(chosen []mutate.Mutation, importers map[string][]string, timeout string, jobs int) tally {
	work, err := os.MkdirTemp("", "mutate-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(2)
	}
	defer func() { _ = os.RemoveAll(work) }()

	// One budget of test processes for the whole run, which measuring coverage draws on too: a
	// mutation's run and each package's coverage each had one of their own, and ten mutations
	// measuring ten tests each were a hundred test processes and as many PostgreSQL containers.
	slots := make(chan struct{}, max(jobs, 1)) // mutate-exempt: how many run at once, not what they say
	// Builds are what cost: go parallelises one across the machine already, so they go one at a
	// time, beside the test processes.
	b := &builds{sem: make(chan struct{}, 1), work: work, bins: map[string]*importerBinary{}}
	covers := &coverages{by: map[string]*coverage{}, timeout: timeout, slots: slots}
	byDir := map[string][]mutate.Mutation{}
	var dirs []string
	for _, m := range chosen {
		dir := filepath.Dir(m.File)
		if byDir[dir] == nil {
			dirs = append(dirs, dir)
		}
		byDir[dir] = append(byDir[dir], m)
	}
	results := make(chan result)
	var wg sync.WaitGroup
	for i, dir := range dirs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := pkgRun{dir: dir, work: filepath.Join(work, strconv.Itoa(i)), timeout: timeout, slots: slots,
				builds: b, importers: importers[filepath.ToSlash(dir)], results: results}
			p.run(byDir[dir], covers)
		}()
	}
	go func() { wg.Wait(); close(results) }()

	t := tally{untestedPackages: map[string]bool{}}
	for r := range results {
		m := r.m
		switch r.outcome {
		case mutate.Killed:
			t.killed++
			fmt.Printf("  refused   %s\n", m)
		case mutate.Unbuildable:
			t.unbuildable++
			fmt.Printf("  no edit   %s (does not build)\n", m)
		case mutate.Untested:
			t.untested++
			t.untestedPackages[filepath.Dir(m.File)] = true
		case mutate.Survived:
			t.survived = append(t.survived, m)
			fmt.Printf("  SURVIVED  %s\n", m)
		}
	}
	sort.Slice(t.survived, func(i, j int) bool { return t.survived[i].String() < t.survived[j].String() })
	return t
}

// pkgRun is one package's mutations put to the tests.
type pkgRun struct {
	dir, work, timeout string
	slots              chan struct{}
	builds             *builds
	importers          []string
	results            chan<- result
}

// run builds the package's schema and puts each mutation in it to the binary, and each it could
// not build to the tests alone, all side by side within the slots.
func (p pkgRun) run(ms []mutate.Mutation, covers *coverages) {
	p.builds.sem <- struct{}{}
	s, err := mutate.Schemata(p.dir, ms, filepath.Join(p.work, "schema"))
	<-p.builds.sem
	if err != nil {
		fmt.Fprintf(os.Stderr, "  (%s: no schema, each edit is built alone: %v)\n", p.dir, err)
		s = &mutate.Schema{Alone: ms}
	}
	fmt.Printf("  %s: %d edit(s) in one binary, %d built alone\n", p.dir, len(s.Built), len(s.Alone))
	// The package as it is says which tests reach a line: the schema moves none.
	var c *mutate.Coverage
	if len(s.Built) > 0 && s.Binary != "" {
		c = covers.of(p.dir)
	}
	var wg sync.WaitGroup
	for _, m := range s.Built {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.results <- result{m, p.built(m, s, c)}
		}()
	}
	for i, m := range s.Alone {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Measured before a slot is taken: the measuring takes slots of its own, and holding
			// one while waiting on the others is how a budget of one never finishes.
			reaching := covers.of(p.dir)
			p.slots <- struct{}{}
			defer func() { <-p.slots }()
			p.results <- result{m, run(m, filepath.Join(p.work, "alone-"+strconv.Itoa(i)), p.timeout, reaching, p.importers)}
		}()
	}
	wg.Wait()
}

// built puts m, which is in s's binary, to the package's tests that reach its line, then to each
// importer's tests, built once with the same schema: a survivor pays for the second.
func (p pkgRun) built(m mutate.Mutation, s *mutate.Schema, c *mutate.Coverage) mutate.Outcome {
	if s.Binary == "" && len(p.importers) == 0 {
		return mutate.Untested
	}
	ask := func(bin, dir string, only []string) mutate.Outcome {
		p.slots <- struct{}{}
		defer func() { <-p.slots }()
		outcome, _ := mutate.Ask(bin, dir, m.ID, p.timeout, only)
		return outcome
	}
	if s.Binary != "" {
		var reaching []string
		if c != nil {
			reaching = c.Reaching(m.File, m.Line)
		}
		if (reaching == nil || len(reaching) > 0) && ask(s.Binary, p.dir, reaching) == mutate.Killed {
			return mutate.Killed
		}
	}
	for _, importer := range p.importers {
		bin, dir := p.builds.importer(p.dir, importer, s.Overlay)
		if bin != "" && ask(bin, dir, nil) == mutate.Killed {
			return mutate.Killed
		}
	}
	return mutate.Survived
}

// builds is the run's one build at a time, and the importers' binaries it made, each once.
type builds struct {
	sem  chan struct{}
	work string
	mu   sync.Mutex
	bins map[string]*importerBinary
}

type importerBinary struct {
	once     sync.Once
	bin, dir string
}

// importer is importer's test binary with the schema of the package in dir, and the directory it
// runs in; an empty binary where it has no tests or would not build, which asks it nothing.
func (b *builds) importer(dir, importer, overlay string) (string, string) {
	key := dir + " " + importer
	b.mu.Lock()
	ib := b.bins[key]
	if ib == nil {
		ib = &importerBinary{}
		b.bins[key] = ib
	}
	n := len(b.bins)
	b.mu.Unlock()
	ib.once.Do(func() {
		b.sem <- struct{}{}
		defer func() { <-b.sem }()
		var err error
		ib.bin, ib.dir, err = mutate.ImporterBinary(importer, overlay, filepath.Join(b.work, "importer-"+strconv.Itoa(n)+".test"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "  (%v)\n", err)
		}
	})
	return ib.bin, ib.dir
}

// run builds the mutation in through an overlay under dir, and runs the package's tests that
// reach its line and then its importers'. The file itself is never written.
func run(m mutate.Mutation, dir, timeout string, c *mutate.Coverage, importers []string) mutate.Outcome {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(2)
	}
	overlay, err := mutate.Overlay(m, dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(2)
	}
	asked := mutate.Asked{Overlay: overlay, Timeout: timeout, Importers: importers}
	if c != nil {
		asked.Reaching = c.Reaching(m.File, m.Line)
	}
	started := time.Now()
	outcome, _ := mutate.Run(m, asked)
	if took := time.Since(started); took > time.Minute {
		fmt.Printf("  (%s took %s)\n", filepath.Dir(m.File), took.Round(time.Second))
	}
	return outcome
}

// coverages is each package's Coverage, measured once and shared by its mutations.
type coverages struct {
	mu      sync.Mutex
	by      map[string]*coverage
	timeout string
	slots   chan struct{}
}

type coverage struct {
	once sync.Once
	c    *mutate.Coverage
}

// of is dir's coverage, or nil where it could not be measured: every test is asked then.
func (cs *coverages) of(dir string) *mutate.Coverage {
	cs.mu.Lock()
	c, ok := cs.by[dir]
	if !ok {
		c = &coverage{}
		cs.by[dir] = c
	}
	cs.mu.Unlock()
	c.once.Do(func() {
		measured, err := mutate.Cover(dir, cs.timeout, cs.slots)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  (%s: no coverage, every test is asked: %v)\n", dir, err)
		}
		c.c = measured
	})
	return c.c
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

// spread takes mutations in rounds, one per function each time round, so a cap covers as many
// of the changed functions as it can rather than the first one exhaustively.
func spread(all []mutate.Mutation, max int) []mutate.Mutation {
	byFunc := map[string][]mutate.Mutation{}
	var order []string
	for _, m := range all {
		key := m.File + ":" + m.Func
		if _, seen := byFunc[key]; !seen {
			order = append(order, key)
		}
		byFunc[key] = append(byFunc[key], m)
	}
	var out []mutate.Mutation
	for round := 0; len(out) < max; round++ {
		took := false
		for _, key := range order {
			if round < len(byFunc[key]) {
				out = append(out, byFunc[key][round])
				took = true
				if len(out) == max {
					return out
				}
			}
		}
		if !took {
			break
		}
	}
	return out
}
