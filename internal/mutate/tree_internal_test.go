package mutate

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// A tree copies the module as git sees it - what it tracks and what it does not ignore - links a
// directory it ignores, leaves out a file it ignores, and after each run puts back whatever the
// run left, changed or removed, naming each by its path in the source, which is never written.
func TestATreeIsTheModuleAsGitSeesItPutBackAfterEachRun(t *testing.T) {
	root := gitModule(t)
	tree := NewTree(t.TempDir())
	var at string
	ran := func(run func(at string)) {
		t.Helper()
		if err := tree.in(filepath.Join(root, "p"), func(at, _ string) { run(at) }); err != nil {
			t.Fatal(err)
		}
	}
	ran(func(dir string) {
		at = dir
		copiedAsGitSeesIt(t, dir)
		// What a run leaves: a file and a directory; a file rewritten at the same size, so only its
		// time says so, and one whose mode alone changed; two removed; a link pointed elsewhere.
		for _, err := range []error{
			os.WriteFile(filepath.Join(dir, "left.txt"), []byte("left"), 0o600),
			os.MkdirAll(filepath.Join(dir, "made/under"), 0o700),
			os.WriteFile(filepath.Join(dir, "tracked.go"), []byte("package q\n"), 0o600),
			os.Chmod(filepath.Join(dir, "mode.txt"), 0o400),
			os.Remove(filepath.Join(dir, "untracked.txt")),
			os.Remove(filepath.Join(dir, "testdata/x.txt")),
			os.Remove(filepath.Join(dir, "link")),
			os.Symlink("elsewhere", filepath.Join(dir, "link")),
		} {
			if err != nil {
				t.Fatal(err)
			}
		}
	})
	var want []string
	for _, name := range []string{"left.txt", "link", "made", "mode.txt", "testdata/x.txt", "tracked.go", "untracked.txt"} {
		want = append(want, filepath.Join(root, "p", name))
	}
	if got := tree.Wrote(); !slices.Equal(got, want) {
		t.Errorf("wrote %v, want %v", got, want)
	}
	putBack(t, root, at)

	// The next run finds it as it was made, and leaves nothing to name.
	ran(func(dir string) {
		if dir != at {
			t.Errorf("the second run is in %s, not the copy already made at %s", dir, at)
		}
	})
	if got := tree.Wrote(); !slices.Equal(got, want) {
		t.Errorf("a run that wrote nothing added to what was written: %v", got)
	}
}

// gitModule is a module in a git work tree: files tracked, one neither tracked nor ignored, an
// ignored file in its package p, and a directory it ignores.
func gitModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":           "module example.com/tree\n\ngo 1.24\n",
		".gitignore":       "out/\n*.log\n",
		"doc.txt":          "tracked, beside the package\n",
		"p/tracked.go":     "package p\n",
		"p/untracked.txt":  "not committed, and not ignored\n",
		"p/skipped.log":    "ignored\n",
		"out/built.txt":    "in an ignored directory\n",
		"p/testdata/x.txt": "a fixture\n",
		"p/mode.txt":       "its mode is changed\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("tracked.go", filepath.Join(root, "p/link")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "go.mod", ".gitignore", "doc.txt", "p/tracked.go", "p/link", "p/testdata"}} {
		git := exec.Command("git", args...)
		git.Dir = root
		if out, err := git.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return root
}

// copiedAsGitSeesIt checks the copy of gitModule's package p at dir.
func copiedAsGitSeesIt(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"tracked.go", "untracked.txt", "testdata/x.txt", "../doc.txt", "../go.mod"} {
		if info, err := os.Lstat(filepath.Join(dir, name)); err != nil || !info.Mode().IsRegular() {
			t.Errorf("%s is not copied: %v", name, err)
		}
	}
	if info, err := os.Lstat(filepath.Join(dir, "../out")); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("the ignored directory is not linked: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "../out/built.txt")); err != nil {
		t.Errorf("the ignored directory is not read through its link: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "skipped.log")); err == nil {
		t.Error("an ignored file was copied")
	}
	if target, err := os.Readlink(filepath.Join(dir, "link")); err != nil || target != "tracked.go" {
		t.Errorf("the link is %q (%v), want the source's own, to tracked.go", target, err)
	}
}

// putBack checks the copy at dir of root's package p is as it was made, and the source as it was.
func putBack(t *testing.T, root, dir string) {
	t.Helper()
	for _, gone := range []string{"left.txt", "made"} {
		if _, err := os.Lstat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("%s was left in the copy", gone)
		}
		if _, err := os.Lstat(filepath.Join(root, "p", gone)); err == nil {
			t.Errorf("%s was written into the source", gone)
		}
	}
	if target, err := os.Readlink(filepath.Join(dir, "link")); err != nil || target != "tracked.go" {
		t.Errorf("the link is %q (%v) after the run, want it put back to tracked.go", target, err)
	}
	if info, err := os.Stat(filepath.Join(dir, "mode.txt")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode.txt is %v (%v) after the run, want its source's 0600", info, err)
	}
	for name, body := range map[string]string{
		"tracked.go": "package p\n", "untracked.txt": "not committed, and not ignored\n", "testdata/x.txt": "a fixture\n",
	} {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != body {
			t.Errorf("%s is %q (%v) in the copy after the run, want it put back", name, got, err)
		}
	}
}
