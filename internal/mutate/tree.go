package mutate

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Tree is a private copy of the modules a run's tests are run in, with a temporary directory of its
// own, held by one test process at a time and put back as its source is after each. A test that writes into its directory - a
// Dockerfile it generates, a file it means to read back - writes into a copy no other run reads.
// Run where the package's sources are, edits asked side by side shared one directory: what one
// wrote, another's tests read, so an edit refused alone was let through beside another, and the
// files stayed in the checkout when the run was over.
//
// The copy is the module as git sees it: its tracked files and those it does not ignore, so a
// test reads testdata/ and ../../docs as it would in place. A directory git ignores (node_modules,
// a build's output) and a repository nested in the module are linked, not copied: read as they
// are, and shared. Outside a git work tree it is every file under the module's root. The tests
// are built where the sources are, and only run here.
type Tree struct {
	dir    string
	copies map[string]*treeCopy // by the root of the module each copies
	wrote  map[string]bool      // what runs left or changed, by its path in the source
}

// NewTree is a tree under dir, which it makes when it is first used.
func NewTree(dir string) *Tree {
	return &Tree{dir: dir, copies: map[string]*treeCopy{}, wrote: map[string]bool{}}
}

// Slots is n trees under dir, each a directory of its own: a run's budget of test processes, and
// where each runs.
func Slots(dir string, n int) chan *Tree {
	slots := make(chan *Tree, n)
	for i := range n {
		slots <- NewTree(filepath.Join(dir, strconv.Itoa(i)))
	}
	return slots
}

// Wrote is every file the tests left, changed or removed in the tree, by its path in the source,
// sorted. None of them was written there.
func (t *Tree) Wrote() []string {
	return slices.Sorted(maps.Keys(t.wrote))
}

// in calls run with where dir, a package's directory, is in the tree, copying its module the
// first time, and with an empty temporary directory, the process's TMPDIR; once run returns, the
// copy is put back as the source is and the temporary directory removed. A test's temporary
// directory is shared as its working directory was: a cache folder under os.TempDir() named by
// the millisecond was one directory for two tests started at once, and each failed with nothing
// broken - asked about no edit, so the edits only they reached were let through.
func (t *Tree) in(dir string, run func(at, tmp string)) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	root, err := moduleRoot(abs)
	if err != nil {
		return err
	}
	c := t.copies[root]
	if c == nil {
		c, err = copyModule(root, filepath.Join(t.dir, strconv.Itoa(len(t.copies))))
		if err != nil {
			return fmt.Errorf("mutate: copying %s to run its tests in: %w", root, err)
		}
		t.copies[root] = c
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return err
	}
	// Not under the tree's own directory: a test's t.TempDir() is its name and two levels more
	// under this, and a socket there, from a test with a long name, was past the 108 bytes a unix
	// socket's path may have - so the test failed with nothing broken, and its lines went unasked.
	tmp, err := os.MkdirTemp("", "m")
	if err != nil {
		return err
	}
	run(filepath.Join(c.dst, rel), tmp)
	if err := os.RemoveAll(tmp); err != nil {
		return fmt.Errorf("mutate: removing the temporary directory of %s's tests: %w", root, err)
	}
	if err := c.reset(func(rel string) { t.wrote[filepath.Join(c.src, rel)] = true }); err != nil {
		return fmt.Errorf("mutate: putting the copy of %s back after its tests: %w", root, err)
	}
	return nil
}

// bin is where a test binary the tree's holder builds goes, beside the copies: one at a time.
func (t *Tree) bin() (string, error) {
	if err := os.MkdirAll(t.dir, 0o700); err != nil {
		return "", err
	}
	bin := filepath.Join(t.dir, "pkg.test")
	// A package with no tests builds none, and the last one's must not be run in its place.
	if err := os.Remove(bin); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return bin, nil
}

// treeCopy is one module copied: each file as it was made, and every directory that holds one.
type treeCopy struct {
	src, dst string
	files    map[string]treeFile
	dirs     map[string]bool
}

// treeFile is a file of the copy as it was made: a symlink's target, or a regular file's stat.
type treeFile struct {
	link    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
}

func copyModule(src, dst string) (*treeCopy, error) {
	files, err := moduleFiles(src)
	if err != nil {
		return nil, err
	}
	c := &treeCopy{src: src, dst: dst, files: map[string]treeFile{}, dirs: map[string]bool{}}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return nil, err
	}
	for _, rel := range files {
		if err := c.put(rel); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// moduleFiles is what of the module under root is copied, by its path from root: what git
// tracks or does not ignore, and each directory it ignores whole, which is linked.
func moduleFiles(root string) ([]string, error) {
	inside := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	inside.Dir = root
	if _, err := output("listing", inside); err != nil {
		return walkFiles(root)
	}
	var out []string
	for _, args := range [][]string{
		{"ls-files", "-z", "--cached", "--others", "--exclude-standard"},
		{"ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory"},
	} {
		list := exec.Command("git", args...)
		list.Dir = root
		said, err := output("listing", list)
		if err != nil {
			return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		ignored := slices.Contains(args, "--ignored")
		for _, path := range strings.Split(string(said), "\x00") {
			dir := strings.HasSuffix(path, "/")
			// An ignored file is a build's or a run's output, which no test reads in place of
			// the source; a directory may be what one does read, node_modules or a checkout.
			if path == "" || ignored && !dir {
				continue
			}
			out = append(out, filepath.FromSlash(strings.TrimSuffix(path, "/")))
		}
	}
	return out, nil
}

// walkFiles is every file under root but git's own.
func walkFiles(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		out = append(out, rel)
		return err
	})
	return out, err
}

// put makes rel in the copy as it is in the source: a regular file copied, a symlink with the
// same target, and a directory - a nested repository, an ignored one - linked to the source's.
func (c *treeCopy) put(rel string) error {
	src, dst := filepath.Join(c.src, rel), filepath.Join(c.dst, rel)
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	info, err := os.Lstat(src)
	if errors.Is(err, fs.ErrNotExist) {
		// Tracked and since deleted, or gone while the run went on: the copy has it no more.
		delete(c.files, rel)
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	var f treeFile
	switch {
	case info.IsDir():
		f.link = src
	case info.Mode()&fs.ModeSymlink != 0:
		if f.link, err = os.Readlink(src); err != nil {
			return err
		}
	case info.Mode().IsRegular():
		if f, err = copyFile(src, dst, info); err != nil {
			return err
		}
	default:
		return nil // a socket or a device: nothing a test reads as a file
	}
	if f.link != "" {
		if err := os.Symlink(f.link, dst); err != nil {
			return err
		}
	}
	c.files[rel] = f
	for d := filepath.Dir(rel); d != "."; d = filepath.Dir(d) {
		c.dirs[d] = true
	}
	return nil
}

// copyFile copies src to dst with its mode and its modification time, which is in the past, so
// any write after is seen by the time alone; the stat it answers is the copy's.
func copyFile(src, dst string, info fs.FileInfo) (treeFile, error) {
	body, err := os.ReadFile(src) //nolint:gosec // a file of the module being copied
	if err != nil {
		return treeFile{}, err
	}
	if err := os.WriteFile(dst, body, info.Mode().Perm()); err != nil {
		return treeFile{}, err
	}
	// WriteFile's mode is under the umask; the copy's is the source's.
	if err := os.Chmod(dst, info.Mode().Perm()); err != nil {
		return treeFile{}, err
	}
	if err := os.Chtimes(dst, info.ModTime(), info.ModTime()); err != nil {
		return treeFile{}, err
	}
	made, err := os.Lstat(dst)
	if err != nil {
		return treeFile{}, err
	}
	return treeFile{size: made.Size(), mode: made.Mode(), modTime: made.ModTime()}, nil
}

// reset puts the copy back as it was made, from the source, and says each file a run left,
// changed or removed.
func (c *treeCopy) reset(wrote func(rel string)) error {
	// What is removed is removed within the copy, whatever a run put in it: a link it made is
	// removed, never followed.
	root, err := os.OpenRoot(c.dst)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	seen := map[string]bool{}
	err = filepath.WalkDir(c.dst, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(c.dst, path)
		if err != nil || rel == "." {
			return err
		}
		if d.IsDir() {
			if c.dirs[rel] {
				return nil
			}
			wrote(rel)
			if err := root.RemoveAll(rel); err != nil {
				return err
			}
			return fs.SkipDir
		}
		f, ok := c.files[rel]
		seen[rel] = true
		if ok && c.unchanged(path, f) {
			return nil
		}
		wrote(rel)
		if !ok {
			return root.Remove(rel)
		}
		return c.put(rel)
	})
	if err != nil {
		return err
	}
	for rel := range c.files {
		if !seen[rel] {
			wrote(rel)
			if err := c.put(rel); err != nil {
				return err
			}
		}
	}
	return nil
}

// unchanged is whether the file at path is still as f says it was made.
func (c *treeCopy) unchanged(path string, f treeFile) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if f.link != "" {
		target, err := os.Readlink(path)
		return err == nil && target == f.link
	}
	return info.Mode() == f.mode && info.Size() == f.size && info.ModTime().Equal(f.modTime)
}
