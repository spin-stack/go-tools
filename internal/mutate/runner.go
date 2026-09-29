package mutate

import (
	"cmp"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Tally is what the tests did about every mutation.
type Tally struct {
	Survived                      []Mutation
	Killed, Unbuildable, Untested int
	UntestedPackages              map[string]bool
}

// result is what the tests did about one mutation.
type result struct {
	m       Mutation
	outcome Outcome
	err     error // the run could not ask the tests at all
}

// RunAll puts every mutation to the tests, jobs at a time, and says each outcome as it comes.
// Each package's mutations are built into one binary (Schemata), run once per mutation;
// an edit the schema could not build is built alone, through an overlay of its own. Each
// package's coverage is measured once, for both.
func RunAll(chosen []Mutation, importers map[string][]string, timeout string, jobs int, w io.Writer) (Tally, error) {
	work, err := os.MkdirTemp("", "mutate-")
	if err != nil {
		return Tally{}, err
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
	byDir := map[string][]Mutation{}
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
				builds: b, importers: importers[filepath.ToSlash(dir)], results: results, out: w}
			p.run(byDir[dir], covers)
		}()
	}
	go func() { wg.Wait(); close(results) }()

	t := Tally{UntestedPackages: map[string]bool{}}
	var firstErr error
	for r := range results {
		m := r.m
		if r.err != nil {
			firstErr = cmp.Or(firstErr, r.err)
			continue
		}
		switch r.outcome {
		case Killed:
			t.Killed++
			_, _ = fmt.Fprintf(w, "  refused   %s\n", m)
		case Unbuildable:
			t.Unbuildable++
			_, _ = fmt.Fprintf(w, "  no edit   %s (does not build)\n", m)
		case Untested:
			t.Untested++
			t.UntestedPackages[filepath.Dir(m.File)] = true
		case Survived:
			t.Survived = append(t.Survived, m)
			_, _ = fmt.Fprintf(w, "  SURVIVED  %s\n", m)
		}
	}
	sort.Slice(t.Survived, func(i, j int) bool { return t.Survived[i].String() < t.Survived[j].String() })
	return t, firstErr
}

// pkgRun is one package's mutations put to the tests.
type pkgRun struct {
	dir, work, timeout string
	slots              chan struct{}
	builds             *builds
	importers          []string
	results            chan<- result
	out                io.Writer
}

// run builds the package's schema and puts each mutation in it to the binary, and each it could
// not build to the tests alone, all side by side within the slots.
func (p pkgRun) run(ms []Mutation, covers *coverages) {
	p.builds.sem <- struct{}{}
	s, err := Schemata(p.dir, ms, filepath.Join(p.work, "schema"))
	<-p.builds.sem
	if err != nil {
		fmt.Fprintf(os.Stderr, "  (%s: no schema, each edit is built alone: %v)\n", p.dir, err)
		s = &Schema{Alone: ms}
	}
	_, _ = fmt.Fprintf(p.out, "  %s: %d edit(s) in one binary, %d built alone\n", p.dir, len(s.Built), len(s.Alone))
	// The package as it is says which tests reach a line: the schema moves none.
	var c *Coverage
	if len(s.Built) > 0 && s.Binary != "" {
		c = covers.of(p.dir)
	}
	var wg sync.WaitGroup
	for _, m := range s.Built {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.results <- result{m: m, outcome: p.built(m, s, c)}
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
			outcome, err := runAlone(m, filepath.Join(p.work, "alone-"+strconv.Itoa(i)), p.timeout, reaching, p.importers, p.out)
			p.results <- result{m: m, outcome: outcome, err: err}
		}()
	}
	wg.Wait()
}

// built puts m, which is in s's binary, to the package's tests that reach its line, then to each
// importer's tests, built once with the same schema: a survivor pays for the second.
func (p pkgRun) built(m Mutation, s *Schema, c *Coverage) Outcome {
	if s.Binary == "" && len(p.importers) == 0 {
		return Untested
	}
	ask := func(bin, dir string, only []string) Outcome {
		p.slots <- struct{}{}
		defer func() { <-p.slots }()
		outcome, _ := Ask(bin, dir, m.ID, p.timeout, only)
		return outcome
	}
	if s.Binary != "" {
		var reaching []string
		if c != nil {
			reaching = c.Reaching(m.File, m.Line)
		}
		if (reaching == nil || len(reaching) > 0) && ask(s.Binary, p.dir, reaching) == Killed {
			return Killed
		}
	}
	for _, importer := range p.importers {
		bin, dir := p.builds.importer(p.dir, importer, s.Overlay)
		if bin != "" && ask(bin, dir, nil) == Killed {
			return Killed
		}
	}
	return Survived
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
		ib.bin, ib.dir, err = ImporterBinary(importer, overlay, filepath.Join(b.work, "importer-"+strconv.Itoa(n)+".test"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "  (%v)\n", err)
		}
	})
	return ib.bin, ib.dir
}

// runAlone builds the mutation in through an overlay under dir, and runs the package's tests that
// reach its line and then its importers'. The file itself is never written.
func runAlone(m Mutation, dir, timeout string, c *Coverage, importers []string, out io.Writer) (Outcome, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	overlay, err := Overlay(m, dir)
	if err != nil {
		return 0, err
	}
	asked := Asked{Overlay: overlay, Timeout: timeout, Importers: importers}
	if c != nil {
		asked.Reaching = c.Reaching(m.File, m.Line)
	}
	started := time.Now()
	outcome, _ := Run(m, asked)
	if took := time.Since(started); took > time.Minute {
		_, _ = fmt.Fprintf(out, "  (%s took %s)\n", filepath.Dir(m.File), took.Round(time.Second))
	}
	return outcome, nil
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
	c    *Coverage
}

// of is dir's coverage, or nil where it could not be measured: every test is asked then.
func (cs *coverages) of(dir string) *Coverage {
	cs.mu.Lock()
	c, ok := cs.by[dir]
	if !ok {
		c = &coverage{}
		cs.by[dir] = c
	}
	cs.mu.Unlock()
	c.once.Do(func() {
		measured, err := Cover(dir, cs.timeout, cs.slots)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  (%s: no coverage, every test is asked: %v)\n", dir, err)
		}
		c.c = measured
	})
	return c.c
}

// Spread takes mutations in rounds, one per function each time round, so a cap covers as many
// of the changed functions as it can rather than the first one exhaustively.
func Spread(all []Mutation, max int) []Mutation {
	byFunc := map[string][]Mutation{}
	var order []string
	for _, m := range all {
		key := m.File + ":" + m.Func
		if _, seen := byFunc[key]; !seen {
			order = append(order, key)
		}
		byFunc[key] = append(byFunc[key], m)
	}
	var out []Mutation
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
