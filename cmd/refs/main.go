// Command refs holds a repository's prose to its tree: every repository path and every `task
// <name>` the files -files names point at must resolve, or be in the allowlist (-allow) with a
// reason. The files are the AGENTS.md and CLAUDE.md an agent loads into every session, by
// default; spin adds its documents.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spin-stack/go-tools/internal/refs"
)

func main() {
	allowPath := flag.String("allow", "", "references made on purpose to something that is not here (none by default)")
	pathspecs := flag.String("files", "AGENTS.md CLAUDE.md */AGENTS.md */CLAUDE.md",
		"the prose to read, as git pathspecs separated by spaces: a tracked file one names is read")
	root := flag.String("root", ".", "repository root")
	taskExe := flag.String("task", taskExeFromEnv(), "the task binary to ask for names")
	flag.Parse()

	if err := run(*root, *allowPath, strings.Fields(*pathspecs), *taskExe); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func taskExeFromEnv() string {
	if v := os.Getenv("TASK_EXE"); v != "" {
		return v
	}
	return "task"
}

func run(root, allowPath string, pathspecs []string, taskExe string) error {
	allow, err := refs.ReadAllow(allowPath)
	if err != nil {
		return err
	}
	// A document either has something holding it to the tree or it is deleted, and this is
	// what holds the ones named.
	files, err := git(root, append([]string{"ls-files", "--"}, pathspecs...)...)
	if err != nil {
		return err
	}
	// A link is the file it points at, read once there: spin-ami's CLAUDE.md is its AGENTS.md.
	files = slices.DeleteFunc(files, func(f string) bool {
		info, err := os.Lstat(filepath.Join(root, f))
		return err == nil && info.Mode()&fs.ModeSymlink != 0
	})
	if len(files) == 0 {
		return fmt.Errorf("refs: git tracks nothing %s names, so this check read nothing", strings.Join(pathspecs, " "))
	}
	tops, err := git(root, "ls-tree", "-d", "--name-only", "HEAD")
	if err != nil {
		return err
	}

	found, err := refs.Check(refs.Options{
		Root:    root,
		Files:   files,
		TopDirs: tops,
		Tasks:   taskLister(root, taskExe),
	})
	if err != nil {
		return err
	}
	unexplained, stale := allow.Split(found)

	fmt.Printf("scanned: %d files    references that do not resolve: %d    explained (%s): %d\n",
		len(files), len(found), allowPath, allow.Len())

	if len(stale) > 0 {
		fmt.Fprint(os.Stderr, "\nstale entries in the allowlist - nothing matches these any more, so the sentence\n"+
			"was rewritten or the thing came back. Remove them.\n")
		for _, key := range stale {
			fmt.Fprintf(os.Stderr, "  %s\n", strings.ReplaceAll(key, "\t", "  ->  "))
		}
	}
	if len(unexplained) > 0 {
		fmt.Fprint(os.Stderr, "\nprose points at something that is not there. An AGENTS.md is loaded into every\n"+
			"session and a document is read by whoever is lost, and neither reader can tell a\n"+
			"stale reference from a load-bearing one:\n\n")
		for _, f := range unexplained {
			fmt.Fprintf(os.Stderr, "  %s  ->  %s\n", f.File, f.Ref)
		}
		fmt.Fprintf(os.Stderr, "\nfix the sentence - or, when it names something absent on purpose, add it to %s\n"+
			"with the reason. That is for a sentence like \"internal/foo was removed; use bar\", or a path in\n"+
			"another repository, or a build output that git ignores: the reference is meant not to resolve.\n", allowPath)
	}
	if len(stale) > 0 || len(unexplained) > 0 {
		return errors.New("refs: the prose and the tree disagree")
	}
	fmt.Println("OK: every path and task this repository's prose names resolves or is explained.")
	return nil
}

// taskLister asks `task` for names as JSON and decodes it as JSON. The shell this replaced
// pulled them out with a sed expression anchored on the pretty listing's layout; when the
// layout moved, it read an empty list and called every reference broken.
func taskLister(root, taskExe string) refs.TaskLister {
	return func(dir string) ([]string, error) {
		// A directory with no Taskfile has no tasks, and that is an answer. Any other error
		// is not: a Taskfile this process cannot read would otherwise report every `task
		// <name>` in that component's prose as broken.
		switch _, err := os.Stat(filepath.Join(root, dir, "Taskfile.yml")); {
		case errors.Is(err, fs.ErrNotExist):
			return nil, nil
		case err != nil:
			return nil, fmt.Errorf("refs: looking for %s: %w", filepath.Join(dir, "Taskfile.yml"), err)
		}
		cmd := exec.Command(taskExe, "-d", dir, "--list-all", "--json") //nolint:gosec // the binary is the gate's own argument
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "NO_COLOR=1")
		out, err := cmd.Output()
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return nil, fmt.Errorf("refs: %s -d %s --list-all --json: %w\n%s", taskExe, dir, err, exit.Stderr)
			}
			return nil, fmt.Errorf("refs: running %s: %w", taskExe, err)
		}
		var listing struct {
			Tasks []struct {
				Name string `json:"name"`
			} `json:"tasks"`
		}
		if err := json.Unmarshal(out, &listing); err != nil {
			return nil, fmt.Errorf("refs: decoding the task list of %s: %w", dir, err)
		}
		names := make([]string, 0, len(listing.Tasks))
		for _, t := range listing.Tasks {
			names = append(names, t.Name)
		}
		return names, nil
	}
}

func git(root string, args ...string) ([]string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("refs: git %s: %w", strings.Join(args, " "), err)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}
