package mutate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
)

// Cache keeps the edits a run's tests refused, so the next run over the same code does not ask
// again: a gate run once more after a fix to one package re-broke every package of the change,
// and ten of its sixteen minutes went to edits whose answer could not have moved.
//
// An edit is kept under a key of everything its answer was read from: the edit itself; every
// file in the directory of each package its tests or its importers' tests are built from in this
// module, and those directories' testdata; the module's go.mod and go.sum, which pin the rest;
// the Go toolchain; and this tool's own version. A file a test reads from outside those - a path
// climbing out of its package - is not in it, which is why only a gate re-run uses a cache and a
// run from scratch (ci.yml's, on main) does not.
//
// Only refusals are kept. A survivor fails the run and is the next one's question; an edit that
// did not build costs a failed build, which is what asking the cache would save.
type Cache struct {
	dir string

	mu      sync.Mutex
	modules map[string]*moduleGraph // by the module's root
	digests map[string]string       // a package directory's files, by directory
	keys    map[int]string          // by Mutation.ID, from Known for Keep
}

// OpenCache is the cache in dir, made if it is not there.
func OpenCache(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mutate: the cache: %w", err)
	}
	return &Cache{dir: dir, modules: map[string]*moduleGraph{}, digests: map[string]string{}, keys: map[int]string{}}, nil
}

// Known splits chosen into the edits to ask the tests about and those a run over the same code
// refused already. importers is Importers'.
func (c *Cache) Known(chosen []Mutation, importers map[string][]string) (fresh, refused []Mutation, err error) {
	for _, m := range chosen {
		key, err := c.key(m, importers[filepath.ToSlash(filepath.Dir(m.File))])
		if err != nil {
			return nil, nil, err
		}
		c.mu.Lock()
		c.keys[m.ID] = key
		c.mu.Unlock()
		if _, err := os.Stat(c.path(key)); err == nil {
			refused = append(refused, m)
			continue
		}
		fresh = append(fresh, m)
	}
	return fresh, refused, nil
}

// Keep records each of refused, which Known was asked about, as refused.
func (c *Cache) Keep(refused []Mutation) error {
	for _, m := range refused {
		c.mu.Lock()
		key, ok := c.keys[m.ID]
		c.mu.Unlock()
		if !ok {
			return fmt.Errorf("mutate: %s was not looked up in the cache", m)
		}
		path := c.path(key)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("mutate: the cache: %w", err)
		}
		if err := os.WriteFile(path, []byte(m.String()+"\n"), 0o600); err != nil {
			return fmt.Errorf("mutate: the cache: %w", err)
		}
	}
	return nil
}

func (c *Cache) path(key string) string {
	return filepath.Join(c.dir, key[:2], key) // mutate-exempt: the store's layout, written and read by this function alone
}

// key is m's, whose package's importers are importers (directories, as Importers names them).
func (c *Cache) key(m Mutation, importers []string) (string, error) {
	dir, err := filepath.Abs(filepath.Dir(m.File))
	if err != nil {
		return "", err
	}
	g, err := c.module(dir)
	if err != nil {
		return "", err
	}
	roots := []string{dir}
	for _, imp := range importers {
		abs, err := filepath.Abs(imp)
		if err != nil { // mutate-exempt: Abs fails only when the working directory is gone, and dir's Abs above would first
			return "", err
		}
		roots = append(roots, abs)
	}
	h := sha256.New()
	file, err := filepath.Rel(g.root, filepath.Join(dir, filepath.Base(m.File)))
	if err != nil {
		return "", err
	}
	// A hash's Write never fails, so neither do these.
	_, _ = fmt.Fprintf(h, "edit %s %d %d %q\n", filepath.ToSlash(file), m.Start, m.End, m.With)
	_, _ = fmt.Fprintf(h, "tool %s\nmodule %s\n", toolVersion(), g.digest)
	for _, d := range g.closure(roots) {
		digest, err := c.dirDigest(d)
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(g.root, d)
		if err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(h, "dir %s %s\n", filepath.ToSlash(rel), digest)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// toolVersion is this binary's module version and sum, which a `go tool` build carries: a new
// version may break an edit differently or read an answer differently.
func toolVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	return bi.Main.Path + "@" + bi.Main.Version + " " + bi.Main.Sum // mutate-exempt: a test binary's main module is the test's, whose version is the same from run to run
}

// dirDigest is the files directly in dir, and everything under its testdata, by name and content.
func (c *Cache) dirDigest(dir string) (string, error) {
	c.mu.Lock()
	digest, ok := c.digests[dir]
	c.mu.Unlock()
	if ok {
		return digest, nil
	}
	h := sha256.New()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("mutate: the cache: %w", err)
	}
	for _, e := range entries {
		if e.Type().IsRegular() {
			if err := hashFile(h, dir, filepath.Join(dir, e.Name())); err != nil {
				return "", err
			}
		}
	}
	testdata := filepath.Join(dir, "testdata")
	err = filepath.WalkDir(testdata, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			return hashFile(h, dir, path)
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("mutate: the cache: %w", err)
	}
	digest = hex.EncodeToString(h.Sum(nil))
	c.mu.Lock()
	c.digests[dir] = digest
	c.mu.Unlock()
	return digest, nil
}

func hashFile(h interface{ Write([]byte) (int, error) }, dir, path string) error {
	body, err := os.ReadFile(path) //nolint:gosec // a file of the module being tested
	if err != nil {
		return fmt.Errorf("mutate: the cache: %w", err)
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	_, err = fmt.Fprintf(h, "%s %x\n", filepath.ToSlash(rel), sum)
	return err
}

// moduleGraph is a module's packages, each by its directory, with the directories of the
// packages of this module it imports and those its tests import.
type moduleGraph struct {
	root, digest      string
	imports, testDeps map[string][]string
}

// module is the graph of the module dir is in, listed once.
func (c *Cache) module(dir string) (*moduleGraph, error) {
	c.mu.Lock()
	g, ok := c.modules[dir]
	c.mu.Unlock()
	if ok {
		return g, nil
	}
	root, err := moduleRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("mutate: the cache: %w", err)
	}
	c.mu.Lock()
	g, ok = c.modules[root]
	c.mu.Unlock()
	if !ok {
		if g, err = listModule(root); err != nil {
			return nil, err
		}
	}
	c.mu.Lock()
	c.modules[root], c.modules[dir] = g, g
	c.mu.Unlock()
	return g, nil
}

func listModule(root string) (*moduleGraph, error) {
	// -e for the reason Importers has it: a package that does not build still names its imports.
	list := exec.Command("go", "list", "-e", "-f",
		"{{.ImportPath}}\t{{.Dir}}\t{{join .Imports \" \"}}\t{{join .TestImports \" \"}} {{join .XTestImports \" \"}}", "./...")
	list.Dir = root
	out, err := output("listing", list)
	if err != nil {
		return nil, fmt.Errorf("mutate: the cache: listing %s: %w", root, err)
	}
	dirOf := map[string]string{}
	type row struct{ dir, imports, tests string }
	var rows []row
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 4 {
			continue
		}
		dirOf[fields[0]] = fields[1]
		rows = append(rows, row{fields[1], fields[2], fields[3]})
	}
	local := func(paths string) []string {
		var dirs []string
		for _, p := range strings.Fields(paths) {
			if d, ok := dirOf[p]; ok {
				dirs = append(dirs, d)
			}
		}
		return dirs
	}
	g := &moduleGraph{root: root, imports: map[string][]string{}, testDeps: map[string][]string{}}
	for _, r := range rows {
		g.imports[r.dir] = local(r.imports)
		g.testDeps[r.dir] = local(r.tests)
	}
	h := sha256.New()
	for _, name := range []string{"go.mod", "go.sum"} {
		if err := hashFile(h, root, filepath.Join(root, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	g.digest = hex.EncodeToString(h.Sum(nil))
	return g, nil
}

// closure is roots and every package of the module their tests are built from, sorted.
func (g *moduleGraph) closure(roots []string) []string {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(d string) {
		if seen[d] {
			return
		}
		seen[d] = true
		for _, i := range g.imports[d] {
			walk(i)
		}
	}
	for _, r := range roots {
		walk(r)
		for _, t := range g.testDeps[r] {
			walk(t)
		}
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}
