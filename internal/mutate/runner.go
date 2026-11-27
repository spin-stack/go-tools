package mutate

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Tally is what the tests did about every mutation.
type Tally struct {
	Survived []Mutation
	// Refused are the refusals a cache keeps: an edit a test failed. Not one stopped at a bound:
	// a stall of a loaded machine stops a test as a hang does, and the memory bound is -mem, by
	// default a share of what was free when the run started, so a refusal at one bound says
	// nothing of a run at another. Kept, either would hide a survivor from every run after.
	// Killed counts them all.
	Refused                       []Mutation
	Killed, Unbuildable, Untested int
	UntestedPackages              map[string]bool
	// Wrote are the files the tests left, changed or removed where they ran, by their paths in
	// the source, which none of them was written to (Tree).
	Wrote []string
	// Broken are the tests that fail with nothing broken, as "<package dir>: <test>", sorted.
	Broken []string
}

// result is what the tests did about one mutation.
type result struct {
	m      Mutation
	answer Answer
	err    error // the run could not ask the tests at all
}

// RunAll puts every mutation to the tests, jobs at a time, and says each outcome as it comes.
// Each package's mutations are built into one binary (Schemata), run once per mutation;
// an edit the schema could not build is built alone, through an overlay of its own. Each
// package's coverage is measured once, for both. Once ctx is cancelled no edit is asked, and the
// run fails with what cancelled it.
func RunAll(ctx context.Context, chosen []Mutation, importers map[string][]string, timeout string, jobs int, w io.Writer) (Tally, error) {
	ceiling, err := time.ParseDuration(timeout)
	if err != nil {
		return Tally{}, fmt.Errorf("mutate: the timeout %q: %w", timeout, err)
	}
	work, err := os.MkdirTemp("", "mutate-")
	if err != nil {
		return Tally{}, err
	}
	defer func() { _ = os.RemoveAll(work) }()

	// One budget of test processes for the whole run, which measuring coverage draws on too: a
	// mutation's run and each package's coverage each had one of their own, and ten mutations
	// measuring ten tests each were a hundred test processes and as many PostgreSQL containers.
	// Each slot is a tree of its own (Tree), which the process holding it runs in.
	slots := Slots(filepath.Join(work, "trees"), max(jobs, 1)) // mutate-exempt: how many run at once, not what they say
	// Builds are what cost: go parallelises one across the machine already, so they go one at a
	// time, beside the test processes.
	b := &builds{sem: make(chan struct{}, 1), work: work, bins: map[string]*importerBinary{}}
	unbroken := NewUnbroken()
	covers := &coverages{by: map[string]*coverage{}, timeout: timeout, slots: slots, unbroken: unbroken}
	byDir, dirs := byPackage(chosen)
	if broken, err := brokenImporters(ctx, dirs, importers, ceiling, slots, unbroken); err != nil {
		return Tally{Broken: broken}, err
	}
	results := make(chan result)
	var wg sync.WaitGroup
	for i, dir := range dirs {
		wg.Go(func() {
			p := pkgRun{dir: dir, work: filepath.Join(work, strconv.Itoa(i)), timeout: timeout, ceiling: ceiling,
				unbroken: unbroken, slots: slots, builds: b, importers: importers[filepath.ToSlash(dir)], results: results, out: w}
			p.run(ctx, byDir[dir], covers)
		})
	}
	go func() { wg.Wait(); close(results) }()

	t := Tally{UntestedPackages: map[string]bool{}}
	var firstErr error
	for r := range results {
		if r.err != nil {
			firstErr = cmp.Or(firstErr, r.err)
			continue
		}
		t.count(r.m, r.answer, w)
	}
	sort.Slice(t.Survived, func(i, j int) bool { return t.Survived[i].String() < t.Survived[j].String() })
	// Every process is done, so every tree is back.
	wrote := map[string]bool{}
	for range cap(slots) {
		for _, path := range (<-slots).Wrote() {
			wrote[path] = true
		}
	}
	t.Wrote = slices.Sorted(maps.Keys(wrote))
	if len(t.Wrote) > 0 {
		_, _ = fmt.Fprintf(w, "  the tests left or changed these in their copy of the module, and not in the source: %s\n",
			strings.Join(t.Wrote, ", "))
	}
	// Every package's coverage is measured by now: each run asked for it before its first edit.
	for dir, c := range covers.by {
		if c.c != nil {
			for test := range c.c.failing {
				t.Broken = append(t.Broken, dir+": "+test)
			}
		}
	}
	slices.Sort(t.Broken)
	if ctx.Err() != nil {
		return t, stoppedEarly(ctx)
	}
	// After every edit was asked, so the run says all it can first. A test that fails with nothing
	// broken is asked about no edit, so what it would have refused is let through, and the run
	// that reports it and passes is a green check over a failing test.
	if len(t.Broken) > 0 && firstErr == nil {
		firstErr = fmt.Errorf("mutate: %d test(s) fail with nothing broken, so no edit was asked of them: %s. "+
			"Each failed in this run's copy of the module, as said above: fix it so that it passes there",
			len(t.Broken), strings.Join(t.Broken, ", "))
	}
	return t, firstErr
}

// count adds what the tests did about m to t, and says it to w.
func (t *Tally) count(m Mutation, a Answer, w io.Writer) {
	switch a.Outcome {
	case Killed, Exceeded, TimedOut:
		t.Killed++
		if a.Outcome == Killed {
			t.Refused = append(t.Refused, m)
		}
		_, _ = fmt.Fprintf(w, "  refused   %s%s\n", m, stoppedBy(a))
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

// stoppedBy is what stopped the tests that refused an edit, where it was a bound and not a test
// that failed: named with it, since the loop or the wait the edit made is worth knowing of.
func stoppedBy(a Answer) string {
	switch {
	case a.Outcome == Exceeded:
		return fmt.Sprintf(" (its tests held over %s, and were stopped)", bound())
	case a.Outcome != TimedOut:
		return ""
	case a.RanPast != "":
		return fmt.Sprintf(" (%s ran past %s, and was stopped)", a.RanPast, a.Allowed.Round(time.Second))
	}
	return fmt.Sprintf(" (its tests ran past %s, and were stopped)", a.Allowed.Round(time.Second))
}

// byPackage is chosen by the directory of its package, and those directories in the order chosen
// first names them.
func byPackage(chosen []Mutation) (map[string][]Mutation, []string) {
	byDir := map[string][]Mutation{}
	var dirs []string
	for _, m := range chosen {
		dir := filepath.Dir(m.File)
		if byDir[dir] == nil {
			dirs = append(dirs, dir)
		}
		byDir[dir] = append(byDir[dir], m)
	}
	return byDir, dirs
}

// brokenImporters runs the tests of every importer of dirs once, on the code as it is, each in one
// of slots' trees, and fails naming each test that fails as "<importer>: <test>", sorted. How long
// each took is what an edit's run of them is allowed a multiple of (Unbroken). A package's own tests
// are measured that way (Cover), and one that fails is asked about no edit; an importer's are
// asked whole, so one that fails regardless refuses every edit asked of it, and the run reports
// none survived. One that does not build asks nothing either way, and is not named.
func brokenImporters(ctx context.Context, dirs []string, importers map[string][]string, ceiling time.Duration, slots chan *Tree, unbroken *Unbroken) ([]string, error) {
	seen := map[string]bool{}
	for _, dir := range dirs {
		for _, importer := range importers[filepath.ToSlash(dir)] {
			seen[importer] = true
		}
	}
	if len(seen) == 0 {
		return nil, nil
	}
	root, err := moduleRoot(".")
	if err != nil {
		return nil, err
	}
	var (
		mu       sync.Mutex
		broken   []string
		firstErr error
		wg       sync.WaitGroup
	)
	for importer := range seen {
		wg.Go(func() {
			tree := <-slots
			defer func() { slots <- tree }()
			var said strings.Builder
			answer, err := runPackage(ctx, tree, importer, filepath.Join(root, importer), root, "an importer's tests, unbroken",
				nil, Allowance{Run: ceiling}, "", &said)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				firstErr = cmp.Or(firstErr, err)
				return
			}
			switch answer.Outcome {
			case Survived:
				unbroken.recordWhole(filepath.Join(root, importer), answer.Took)
				unbroken.recordTests(filepath.Join(root, importer), testTimes(answer.Said))
				return
			case Unbuildable:
				return
			}
			// mutate-exempt: how much of the failure is shown, not whether it is.
			fmt.Fprintf(os.Stderr, "  (%s fails with nothing broken:)\n%s\n", importer, tail([]byte(said.String()), 20))
			failed := failedTests(said.String())
			switch {
			case answer.Outcome == TimedOut:
				failed = append(failed, fmt.Sprintf("its tests, which ran past %s", answer.Allowed))
			case len(failed) == 0:
				failed = []string{"its test binary, which named no test"}
			}
			for _, test := range failed {
				broken = append(broken, importer+": "+test)
			}
		})
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, stoppedEarly(ctx)
	}
	if firstErr != nil {
		return nil, firstErr
	}
	if len(broken) == 0 {
		return nil, nil
	}
	slices.Sort(broken)
	return broken, fmt.Errorf("mutate: %d importer test(s) fail with nothing broken, so they would refuse every edit "+
		"asked of them: %s. Each failed in this run's copy of the module, as said above: fix it so that it passes there",
		len(broken), strings.Join(broken, ", "))
}

// failedTests is the top-level tests a test binary's -test.v output says failed, in order.
func failedTests(out string) []string {
	var failed []string
	for line := range strings.SplitSeq(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "--- FAIL: "); ok {
			name, _, _ := strings.Cut(rest, " ")
			failed = append(failed, name)
		}
	}
	return failed
}

// pkgRun is one package's mutations put to the tests.
type pkgRun struct {
	dir, work, timeout string
	// ceiling is timeout, which no run of tests passes; unbroken is what each is allowed a
	// multiple of.
	ceiling   time.Duration
	unbroken  *Unbroken
	slots     chan *Tree
	builds    *builds
	importers []string
	results   chan<- result
	out       io.Writer
}

// run builds the package's schema and puts each mutation in it to the binary, and each it could
// not build to the tests alone, all side by side within the slots.
func (p pkgRun) run(ctx context.Context, ms []Mutation, covers *coverages) {
	p.builds.sem <- struct{}{}
	s, err := Schemata(ctx, p.dir, ms, filepath.Join(p.work, "schema"))
	<-p.builds.sem
	if ctx.Err() != nil {
		for _, m := range ms {
			p.results <- result{m: m, err: ctx.Err()}
		}
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "  (%s: no schema, each edit is built alone: %v)\n", p.dir, err)
		s = &Schema{Alone: ms}
	}
	_, _ = fmt.Fprintf(p.out, "  %s: %d edit(s) in one binary, %d built alone\n", p.dir, len(s.Built), len(s.Alone))
	// The package as it is says which tests reach a line: the schema moves none.
	var c *Coverage
	if len(s.Built) > 0 && s.Binary != "" { // mutate-exempt: with no edit built, only one built alone reads it, and measures it itself
		c = covers.of(ctx, p.dir)
	}
	var wg sync.WaitGroup
	for _, m := range s.Built {
		wg.Go(func() {
			answer, err := p.built(ctx, m, s, c)
			p.results <- stopped(ctx, result{m: m, answer: answer, err: err})
		})
	}
	for i, m := range s.Alone {
		wg.Go(func() {
			// Measured before a slot is taken: the measuring takes slots of its own, and holding
			// one while waiting on the others is how a budget of one never finishes.
			reaching := covers.of(ctx, p.dir)
			tree := <-p.slots
			defer func() { p.slots <- tree }()
			answer, err := runAlone(ctx, m, filepath.Join(p.work, "alone-"+strconv.Itoa(i)),
				Asked{Timeout: p.timeout, Unbroken: p.unbroken, Importers: p.importers, Tree: tree}, reaching, p.out)
			p.results <- stopped(ctx, result{m: m, answer: answer, err: err})
		})
	}
	wg.Wait()
}

// stopped is r, failed with ctx's error once ctx is cancelled: what the tests did then may be the
// cancellation's doing - an importer it kept from building asks nothing, and the edit reads as
// one no test refused.
func stopped(ctx context.Context, r result) result {
	if err := ctx.Err(); err != nil {
		return result{m: r.m, err: err}
	}
	return r
}

// stoppedEarly is how a run cancelled before it asked every edit fails.
func stoppedEarly(ctx context.Context) error {
	return fmt.Errorf("mutate: stopped before every edit was asked: %w", ctx.Err())
}

// built puts m, which is in s's binary, to the package's tests that reach it, then to each
// importer's tests, built once with the same schema: a survivor pays for the second.
func (p pkgRun) built(ctx context.Context, m Mutation, s *Schema, c *Coverage) (Answer, error) {
	if s.Binary == "" && len(p.importers) == 0 {
		return Answer{Outcome: Untested}, nil
	}
	ask := func(bin, dir string, only []string) (Answer, error) {
		tree := <-p.slots
		defer func() { p.slots <- tree }()
		return Ask(ctx, tree, bin, dir, m.ID, p.unbroken.allowance(dir, only, p.ceiling), only)
	}
	if s.Binary != "" {
		var reaching []string
		if c != nil {
			reaching = c.Reaching(m)
		}
		if reaching == nil || len(reaching) > 0 {
			if answer, err := ask(s.Binary, p.dir, reaching); err != nil || answer.Outcome != Survived {
				return answer, err
			}
		}
	}
	for _, importer := range p.importers {
		bin, dir := p.builds.importer(ctx, p.dir, importer, s.Overlay)
		if bin == "" {
			continue
		}
		if answer, err := ask(bin, dir, nil); err != nil || answer.Outcome != Survived {
			return answer, err
		}
	}
	return Answer{Outcome: Survived}, nil
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
func (b *builds) importer(ctx context.Context, dir, importer, overlay string) (string, string) {
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
		ib.bin, ib.dir, err = ImporterBinary(ctx, importer, overlay, filepath.Join(b.work, "importer-"+strconv.Itoa(n)+".test"))
		if err != nil { // mutate-exempt: what is said on stderr; an importer with no binary asks nothing either way
			fmt.Fprintf(os.Stderr, "  (%v)\n", err)
		}
	})
	return ib.bin, ib.dir
}

// runAlone builds the mutation in through an overlay under dir, and asks it as asked says, of the
// package's tests that reach it by c and then its importers'. The file itself is never
// written.
func runAlone(ctx context.Context, m Mutation, dir string, asked Asked, c *Coverage, out io.Writer) (Answer, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Answer{}, err
	}
	overlay, err := Overlay(m, dir)
	if err != nil {
		return Answer{}, err
	}
	asked.Overlay = overlay
	if c != nil {
		asked.Reaching = c.Reaching(m)
	}
	started := time.Now()
	answer, err := Run(ctx, m, asked)
	if err != nil {
		return Answer{}, err
	}
	if took := time.Since(started); took > time.Minute {
		_, _ = fmt.Fprintf(out, "  (%s took %s)\n", m, took.Round(time.Second))
	}
	return answer, nil
}

// coverages is each package's Coverage, measured once and shared by its mutations.
type coverages struct {
	mu       sync.Mutex
	by       map[string]*coverage
	timeout  string
	slots    chan *Tree
	unbroken *Unbroken
}

type coverage struct {
	once sync.Once
	c    *Coverage
}

// of is dir's coverage, or nil where it could not be measured: every test is asked then.
func (cs *coverages) of(ctx context.Context, dir string) *Coverage {
	cs.mu.Lock()
	c, ok := cs.by[dir]
	if !ok {
		c = &coverage{}
		cs.by[dir] = c
	}
	cs.mu.Unlock()
	c.once.Do(func() {
		measured, err := Cover(ctx, dir, cs.timeout, cs.slots)
		if err != nil { // mutate-exempt: what is said on stderr; the coverage is nil either way
			fmt.Fprintf(os.Stderr, "  (%s: no coverage, every test is asked: %v)\n", dir, err)
		}
		c.c = measured
		if measured != nil {
			cs.unbroken.recordTests(dir, measured.took)
		}
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
