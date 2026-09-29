package refs_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/spin-stack/go-tools/internal/allow"
	"github.com/spin-stack/go-tools/internal/refs"
)

// tree writes a repository for one case to read. Built here rather than committed, because
// what this analyzer answers is "does this path exist", and a fixture directory that git
// cannot hold empty would answer it by accident.
func tree(t *testing.T, prose string) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"internal/db/queries", "docs/network", "cmd/cli"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"internal/db/queries/fleet.sql", "docs/network/README.md", "cmd/cli/main.go"} {
		if err := os.WriteFile(filepath.Join(root, file), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte(prose), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func check(t *testing.T, root string, tasks refs.TaskLister) []refs.Finding {
	t.Helper()
	found, err := refs.Check(refs.Options{
		Root:    root,
		Files:   []string{"CLAUDE.md"},
		TopDirs: []string{"internal", "docs", "cmd"},
		Tasks:   tasks,
	})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	return found
}

func onlyRoot(names ...string) refs.TaskLister {
	return func(dir string) ([]string, error) {
		if dir == "." {
			return names, nil
		}
		return nil, nil
	}
}

func keys(found []refs.Finding) []string {
	out := make([]string, len(found))
	for i, f := range found {
		out[i] = f.Ref
	}
	return out
}

func wants(t *testing.T, got []refs.Finding, want ...string) {
	t.Helper()
	g := keys(got)
	if len(g) != len(want) {
		t.Fatalf("findings are %v, wanted %v", g, want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("findings are %v, wanted %v", g, want)
		}
	}
}

// What it must catch.
func TestAPathThatIsNotThereIsReported(t *testing.T) {
	root := tree(t, "The queries live in `internal/db/queries` and the loop in `internal/sweep`.\n")
	wants(t, check(t, root, onlyRoot("build")), "internal/sweep")
}

func TestATaskThatIsNotThereIsReported(t *testing.T) {
	root := tree(t, "Run `task build` before committing, then `task deploy:prod`.\n")
	wants(t, check(t, root, onlyRoot("build")), "task deploy:prod")
}

// A reference is one finding however often the prose makes it, and the findings are in order.
func TestAReferenceMadeTwiceIsOneFinding(t *testing.T) {
	root := tree(t, "See `docs/zz.md`, then `docs/aa.md`, and `docs/aa.md` again.\n")
	wants(t, check(t, root, onlyRoot("build")), "docs/aa.md", "docs/zz.md")
}

func TestAGlobThatMatchesNothingIsReported(t *testing.T) {
	root := tree(t, "Queries are `internal/db/queries/*.sql`; the pictures are `docs/storage/*.dot`.\n")
	wants(t, check(t, root, onlyRoot("build")), "docs/storage/*.dot")
}

// What it must leave alone. Each of these was a way the shell version could have gone wrong,
// and a check that only catches is a check that fails the tree for being written in English.
func TestAModulePathIsNotAMissingDirectory(t *testing.T) {
	// The module path carries `/cmd/buf`, and this repository has a `cmd/` - so a pattern
	// that does not care what precedes the match reports a `cmd/buf` nobody is missing.
	root := tree(t, "buf is installed from github.com/bufbuild/buf/cmd/buf and pinned in the Taskfile.\n")
	wants(t, check(t, root, onlyRoot("build")))
}

func TestASymbolYieldsItsPackage(t *testing.T) {
	// `internal/db/queries.Fleet` is the package plus a symbol: the package exists, and the
	// dot must not make it a file nobody named.
	root := tree(t, "See `internal/db/queries.Fleet` and `cmd/cli.Version`.\n")
	wants(t, check(t, root, onlyRoot("build")))
}

// A name of the other repositories' kinds is a file, whole: host.config is not a package host,
// and apko.lock.json is not apko.lock.
func TestAFileOfAnyKindTheRepositoriesNameIsWhole(t *testing.T) {
	root := tree(t, "The kernel asserts `cmd/cli/host.config`; `task lock` writes `cmd/cli/apko.lock.json`.\n")
	for _, f := range []string{"cmd/cli/host.config", "cmd/cli/apko.lock.json"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wants(t, check(t, root, onlyRoot("lock")))
}

// A lane suffix is part of the name, not a symbol after it. deploy/Caddyfile.e2e read as
// deploy/Caddyfile is a file that exists reported as missing, and a gate that cries wolf is
// worth less than one that misses something: the first gets turned off.
func TestALaneSuffixIsPartOfTheFilename(t *testing.T) {
	root := tree(t, "x")
	if err := os.WriteFile(filepath.Join(root, "cmd/cli/config.e2e"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd/cli/config.dev"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"),
		[]byte("The lanes read `cmd/cli/config.e2e` and `cmd/cli/config.dev`.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wants(t, check(t, root, onlyRoot("build")))
}

func TestAGlobstarWalksBelowItsDirectory(t *testing.T) {
	root := tree(t, "Everything under `docs/**/*` is checked.\n")
	wants(t, check(t, root, onlyRoot("build")))
}

func TestAFlagIsNotATaskName(t *testing.T) {
	root := tree(t, "`task --list` is the catalogue.\n")
	wants(t, check(t, root, onlyRoot("build")))
}

// A component's file says "generate is a root task", and the root's list has to answer for it.
func TestAComponentResolvesAgainstItsOwnTaskfileAndTheRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "storage"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "storage/CLAUDE.md"),
		[]byte("`task ci:full` is the merge gate; `task generate` is a root task; `task nope` is not.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	found, err := refs.Check(refs.Options{
		Root:    root,
		Files:   []string{"storage/CLAUDE.md"},
		TopDirs: []string{"storage"},
		Tasks: func(dir string) ([]string, error) {
			switch dir {
			case ".":
				return []string{"generate"}, nil
			case "storage":
				return []string{"ci:full"}, nil
			}
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	wants(t, found, "task nope")
}

// The failure this package was extracted for. The shell read the task list with a sed
// expression; the day it matched nothing, every `task <name>` in the tree was reported broken
// and the report was believed.
func TestAnEmptyTaskListIsARefusalAndNotAnAnswer(t *testing.T) {
	root := tree(t, "Run `task build`.\n")
	_, err := refs.Check(refs.Options{
		Root:    root,
		Files:   []string{"CLAUDE.md"},
		TopDirs: []string{"internal"},
		Tasks:   func(string) ([]string, error) { return nil, nil },
	})
	if !errors.Is(err, refs.ErrNoTasks) {
		t.Fatalf("an empty task list gave %v, and should refuse: every reference would be reported broken", err)
	}
}

// Both directions of the ratchet, on one tree: one reference explained, one entry that
// explains nothing.
func TestTheAllowlistTurnsBothWays(t *testing.T) {
	root := tree(t, "`internal/sweep` is gone, and `internal/audit` too.\n")
	allowPath := filepath.Join(root, "allow.txt")
	body := "# why this exists\n" +
		"CLAUDE.md\tinternal/sweep  # named to say it is gone\n" +
		"CLAUDE.md\tinternal/ancient  # nothing says this any more\n"
	if err := os.WriteFile(allowPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	allowed, err := allow.Read(allowPath)
	if err != nil {
		t.Fatalf("read allow: %v", err)
	}

	unexplained, stale := allow.Split(allowed, check(t, root, onlyRoot("build")), refs.Finding.Key)
	wants(t, unexplained, "internal/audit")
	if len(stale) != 1 || stale[0] != "CLAUDE.md\tinternal/ancient" {
		t.Fatalf("stale entries are %q, wanted the one nothing matches", stale)
	}
}

// A path git ignores is what a build left behind, not part of the tree, and a reference to it
// does not resolve whether or not the build has run. Read off the disk instead, the answer was
// whichever order the steps ran in: CI passed where the dashboard had been built first and
// failed in a job that had not built it.
func TestABuildOutputIsNotInTheTreeWhetherOrNotItWasBuilt(t *testing.T) {
	root := tree(t, "The build copies it into `cmd/cli/web`, which is also named `cmd/cli/main.go`.\n")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("cmd/cli/web/\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	wants(t, check(t, root, onlyRoot("build")), "cmd/cli/web")

	// Built: on the disk now, and still not the tree's.
	if err := os.MkdirAll(filepath.Join(root, "cmd", "cli", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	wants(t, check(t, root, onlyRoot("build")), "cmd/cli/web")
}
