// Package gencache is go generate for a module's packages, with each `go tool mockgen` directive
// answered from a cache when nothing its output depends on has changed since it last ran.
//
// go generate has no cache: spin's 135 mockgen directives took 80 s of every CI run, one after
// another, each loading the packages it mocks with their types, for mocks no change had touched.
package gencache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Options is one run: go generate's packages and tags, in a module's directory.
type Options struct {
	Dir      string // where the go command runs; the module's root
	Tags     string // -tags, as go generate takes it
	Patterns []string
	Cache    string    // the cache's directory
	Jobs     int       // packages generated side by side; a package's directives run in order
	Out      io.Writer // what ran, and what it printed
}

// Result counts the directives by how they were answered.
type Result struct {
	Directives int
	FromCache  int // the cache had the mock: its bytes are the file's
	Rewritten  int // of FromCache, files whose bytes on disk were not those
	Ran        int // keyed and not in the cache: run, and its output kept
	// NotKeyed are the directives run because their output depends on something the key does
	// not cover, by that reason.
	NotKeyed map[string]int
}

// pkg is what `go list -json` says of a package, the fields gencache reads.
type pkg struct {
	ImportPath   string   `json:"ImportPath"`
	Dir          string   `json:"Dir"`
	Name         string   `json:"Name"`
	GoFiles      []string `json:"GoFiles"`
	CgoFiles     []string `json:"CgoFiles"`
	TestGoFiles  []string `json:"TestGoFiles"`
	XTestGoFiles []string `json:"XTestGoFiles"`
	Standard     bool     `json:"Standard"`
	Deps         []string `json:"Deps"`
	Module       *module  `json:"Module"`
	Error        *struct {
		Err string `json:"Err"`
	} `json:"Error"`
}

type module struct {
	Path    string `json:"Path"`
	Version string `json:"Version"`
	Main    bool   `json:"Main"`
	Replace *struct {
		Path    string `json:"Path"`
		Version string `json:"Version"`
	} `json:"Replace"`
}

// step is a directive and, when it can be keyed, what its mockgen reads; reason says why not.
type step struct {
	directive
	m      mockgen
	dest   string
	roots  []string // the packages mockgen reads the mock from, by import path
	reason string
}

type run struct {
	o     Options
	env   map[string]string
	base  []string // the key's lines every directive shares
	read  map[string]*pkg
	cache cache

	// mocks are the files the keyed directives write. None is in any key: a mock is made from
	// what its own key covers, and mockgen reads no interface from one.
	mocks map[string]bool

	mu    sync.Mutex
	tools map[string]toolBuild // mockgen's build, by the name `go tool` was given
	own   map[dirKey]string
	res   Result
}

type toolBuild struct {
	id  string
	err error
}

// dirKey is what of a directory was hashed: with its tests or without.
type dirKey struct {
	dir   string
	tests bool
}

// Run generates the packages and says how each directive was answered. A directive that fails
// stops its package's later directives, as go generate stops; other packages go on.
func Run(ctx context.Context, o Options) (Result, error) {
	if len(o.Patterns) == 0 {
		o.Patterns = []string{"./..."}
	}
	if o.Jobs < 1 { // mutate-exempt: how many packages run side by side, which no output depends on
		o.Jobs = 1 // mutate-exempt: as above
	}
	r := &run{o: o, cache: cache{dir: o.Cache}, mocks: map[string]bool{}, tools: map[string]toolBuild{}, own: map[dirKey]string{}}
	r.res.NotKeyed = map[string]int{}
	if err := r.cache.trim(); err != nil {
		return r.res, err
	}
	if err := r.goEnv(ctx); err != nil {
		return r.res, err
	}
	groups, err := r.discover(ctx)
	if err != nil {
		return r.res, err
	}

	var (
		wg   sync.WaitGroup
		errs = make([]error, len(groups))
		sem  = make(chan struct{}, o.Jobs)
	)
	for i, group := range groups {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			for _, s := range group {
				if err := r.generate(ctx, s); err != nil {
					errs[i] = err
					return
				}
			}
		})
	}
	wg.Wait()
	return r.res, errors.Join(errs...)
}

// discover reads the directives of the packages go generate would read, under its tags, a
// group a package, and loads what their mocks are read from.
func (r *run) discover(ctx context.Context) ([][]step, error) {
	args := []string{"list", "-e", "-json=ImportPath,Dir,Name,GoFiles,CgoFiles,TestGoFiles,XTestGoFiles,Standard"}
	if r.o.Tags != "" {
		args = append(args, "-tags", r.o.Tags)
	}
	pkgs, err := r.list(ctx, append(args, r.o.Patterns...))
	if err != nil {
		return nil, err
	}
	var groups [][]step
	roots := map[string]bool{}
	for _, p := range pkgs {
		if p.Standard {
			continue
		}
		var group []step
		for _, set := range []struct {
			files []string
			name  string
		}{{p.GoFiles, p.Name}, {p.CgoFiles, p.Name}, {p.TestGoFiles, p.Name}, {p.XTestGoFiles, p.Name + "_test"}} {
			for _, f := range set.files {
				ds, err := directives(p, f, set.name, r.env)
				if err != nil {
					return nil, err
				}
				for _, d := range ds {
					s := plan(d)
					for _, root := range s.roots {
						roots[root] = true
					}
					r.mocks[s.dest] = true
					group = append(group, s)
				}
			}
		}
		if len(group) > 0 {
			groups = append(groups, group)
			r.res.Directives += len(group)
		}
	}
	return groups, r.load(ctx, roots)
}

// load is what mockgen reads the mocks from, as mockgen reads it: go list without go
// generate's tags, which the generator is not given.
func (r *run) load(ctx context.Context, roots map[string]bool) error {
	r.read = map[string]*pkg{}
	if len(roots) == 0 { // mutate-exempt: go list given no package lists the directory's, which no key reads
		return nil
	}
	args := []string{"list", "-e", "-deps", "-json=ImportPath,Dir,Standard,Deps,Module,Error"}
	for root := range roots {
		args = append(args, root)
	}
	read, err := r.list(ctx, args)
	if err != nil {
		return err
	}
	for _, p := range read {
		r.read[p.ImportPath] = p
	}
	return nil
}

// plan says what a directive's mockgen reads, or why it cannot be keyed.
func plan(d directive) step {
	s := step{directive: d, reason: "go generate could not read it"}
	switch {
	case d.aliases != nil:
		s.reason = "a -command alias in its file"
		return s
	case d.words == nil:
		return s
	}
	s.m, s.reason = parseMockgen(d.words)
	if s.reason != "" {
		return s
	}
	s.dest = abs(d.pkg.Dir, s.m.destination)
	if s.m.source == "" {
		root := s.m.pkgPath
		if strings.HasPrefix(root, ".") {
			root = path.Join(d.pkg.ImportPath, root)
		}
		s.roots = []string{root}
		return s
	}
	// Source mode parses the file, and every package it names when an interface embeds one.
	src := abs(d.pkg.Dir, s.m.source)
	f, err := parser.ParseFile(token.NewFileSet(), src, nil, parser.ImportsOnly)
	if err != nil {
		s.reason = "its -source does not parse"
		return s
	}
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p != "C" {
			s.roots = append(s.roots, p)
		}
	}
	return s
}

func (r *run) generate(ctx context.Context, s step) error {
	key := ""
	if s.reason == "" {
		key, s.reason = r.key(ctx, s)
	}
	if s.reason != "" {
		r.count(func(res *Result) { res.NotKeyed[s.reason]++ })
		r.sayf("run, not cached (%s): %s", s.reason, s.directive)
		err := r.goGenerate(ctx, s.directive)
		// What it wrote is not known, so no directory's hash is either.
		r.mu.Lock()
		r.own = map[dirKey]string{}
		r.mu.Unlock()
		return err
	}

	if b, ok := r.cache.get(key); ok {
		cur, err := os.ReadFile(s.dest)
		if err != nil || !bytes.Equal(cur, b) {
			if err := writeFile(s.dest, b); err != nil {
				return err
			}
			r.count(func(res *Result) { res.Rewritten++ })
			r.sayf("from the cache: %s", s.dest)
		}
		r.count(func(res *Result) { res.FromCache++ })
		return nil
	}
	r.sayf("run: %s", s.directive)
	if err := r.goGenerate(ctx, s.directive); err != nil {
		return err
	}
	b, err := os.ReadFile(s.dest)
	if err != nil {
		return fmt.Errorf("%s: mockgen wrote no %s: %w", s.directive, s.dest, err)
	}
	r.count(func(res *Result) { res.Ran++ })
	return r.cache.put(key, b)
}

// key is the hash of everything a mock's bytes can depend on: the directive, mockgen's build,
// the toolchain and the build's configuration, and every package mockgen reads it from. "" and
// why, when one of those is not known.
func (r *run) key(ctx context.Context, s step) (string, string) {
	tool, err := r.tool(ctx, s.m.tool)
	if err != nil {
		return "", "go tool -n " + s.m.tool + " failed"
	}
	h := sha256.New()
	line := func(s string) { _, _ = io.WriteString(h, s+"\n") }
	for _, b := range r.base {
		line(b)
	}
	line("tool " + tool)
	line("package " + s.pkg.ImportPath)
	words := make([]string, len(s.words))
	for i, w := range s.words {
		words[i] = strconv.Quote(w)
	}
	line("words " + strings.Join(words, " "))

	// Package mode type-checks the package, which never reads its tests. Source mode parses
	// whole directories with go/parser - every .go file, tests too, whatever its build
	// constraint - starting with the -source file's own.
	//
	// A directory is named in the key by its package's import path, never where it is, so a
	// checkout elsewhere - CI's, a worktree, another module cache - has the same keys.
	tests := s.m.source != ""
	dirs := map[string]string{} // the directory, by its name in the key
	if tests {
		src := filepath.Dir(abs(s.pkg.Dir, s.m.source))
		rel, err := filepath.Rel(s.pkg.Dir, src)
		if err != nil {
			return "", "its -source is nowhere the package can name"
		}
		dirs[s.pkg.ImportPath+"/"+filepath.ToSlash(rel)] = src
	}
	for _, root := range s.roots {
		p := r.read[root]
		if p == nil {
			return "", "go list cannot load a package it reads"
		}
		for _, dep := range append([]string{root}, p.Deps...) {
			q := r.read[dep]
			if q == nil || q.Error != nil {
				return "", "go list cannot load a package it reads"
			}
			dirs[dep] = q.Dir
		}
	}
	names := make([]string, 0, len(dirs))
	for name := range dirs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		own, err := r.dirHash(dirs[name], tests)
		if err != nil {
			return "", "a file it reads cannot be read"
		}
		line("dir " + name + " " + own)
	}
	return hex.EncodeToString(h.Sum(nil)), ""
}

// dirHash is the hash of a directory's .go files - every one, whatever its build constraint,
// since a file the build leaves out changes the key and never a mock - and its tests' when
// they are read. No mock is one of them, so a mock written beside what it mocks does not key
// itself.
func (r *run) dirHash(dir string, tests bool) (string, error) {
	memo := dirKey{dir: dir, tests: tests}
	r.mu.Lock()
	own, ok := r.own[memo]
	r.mu.Unlock()
	if ok {
		return own, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, e := range entries {
		name := e.Name()
		path := filepath.Join(dir, name)
		if e.IsDir() || !strings.HasSuffix(name, ".go") || r.mocks[path] || (!tests && strings.HasSuffix(name, "_test.go")) {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(b)
		_, _ = fmt.Fprintf(h, "%s %x\n", name, sum)
	}
	own = hex.EncodeToString(h.Sum(nil))
	r.mu.Lock()
	r.own[memo] = own
	r.mu.Unlock()
	return own, nil
}

// goGenerate runs one directive through go generate itself, so what runs is what go generate
// would run: its expansion, its environment, its -command aliases.
func (r *run) goGenerate(ctx context.Context, d directive) error {
	var alts []string
	for _, a := range d.aliases {
		alts = append(alts, regexp.QuoteMeta(a))
	}
	alts = append(alts, regexp.QuoteMeta(d.text))
	args := []string{"generate"}
	if r.o.Tags != "" {
		args = append(args, "-tags", r.o.Tags)
	}
	args = append(args, "-run", "^("+strings.Join(alts, "|")+")$", ".")
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = d.pkg.Dir
	out, err := cmd.CombinedOutput()
	r.write(out)
	if err != nil {
		return fmt.Errorf("%s: %w", d, err)
	}
	return nil
}

// tool is a `go tool` binary's build, asked once a run.
func (r *run) tool(ctx context.Context, name string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tools[name]; ok {
		return t.id, t.err
	}
	cmd := exec.CommandContext(ctx, "go", "tool", "-n", name)
	cmd.Dir = r.o.Dir
	out, err := cmd.Output()
	// The binary's directory in the build cache is named for the hash of what built it, and
	// that name, not where the cache is, is the build.
	t := toolBuild{id: filepath.Base(filepath.Dir(strings.TrimSpace(string(out)))), err: err}
	r.tools[name] = t
	return t.id, t.err
}

// goEnv reads what the key takes from the go command: the toolchain, the target, and what
// changes which files a build reads or how it reads them.
func (r *run) goEnv(ctx context.Context) error {
	keyed := []string{"GOVERSION", "GOOS", "GOARCH", "GOFLAGS", "CGO_ENABLED", "GOEXPERIMENT"}
	// GOROOT is not keyed: a directive's $GOROOT expands to it, and the toolchain is GOVERSION.
	cmd := exec.CommandContext(ctx, "go", append([]string{"env", "-json", "GOROOT"}, keyed...)...)
	cmd.Dir = r.o.Dir // mutate-exempt: the module's toolchain line picks GOVERSION, and the tests' module picks the one running them
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("go env: %w", stderr(err))
	}
	if err := json.Unmarshal(out, &r.env); err != nil {
		return fmt.Errorf("go env: %w", err)
	}
	cmd = exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{.GoVersion}}")
	cmd.Dir = r.o.Dir
	goLine, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("go list -m: %w", stderr(err))
	}
	r.base = []string{"gencache 1", "module go " + strings.Join(strings.Fields(string(goLine)), " ")}
	for _, n := range keyed {
		r.base = append(r.base, n+" "+r.env[n])
	}
	return nil
}

func (r *run) list(ctx context.Context, args []string) ([]*pkg, error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = r.o.Dir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", stderr(err))
	}
	var pkgs []*pkg
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		p := new(pkg)
		if err := dec.Decode(p); err != nil {
			return nil, fmt.Errorf("go list: %w", err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

func (r *run) count(f func(*Result)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f(&r.res)
}

func (r *run) sayf(format string, args ...any) {
	r.write(fmt.Appendf(nil, format+"\n", args...))
}

// write is what a generator printed, whole, so two packages' lines do not interleave.
func (r *run) write(b []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = r.o.Out.Write(b) // what was printed is not the generated code
}

func abs(dir, file string) string {
	if filepath.IsAbs(file) {
		return file
	}
	return filepath.Join(dir, file)
}

func stderr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Errorf("%w: %s", err, bytes.TrimSpace(ee.Stderr))
	}
	return err
}

// writeFile replaces a file's bytes in one rename, keeping its mode.
func writeFile(path string, b []byte) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".gencache-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
