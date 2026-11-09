// Command task is Task at the version a repository's versions.yaml pins, with go-tools' commands
// on its PATH: `go tool task lint` runs the released Task, and a Taskfile says `mutate` where it
// would say `go tool mutate`.
//
//	go tool task [Task's arguments]
//
// A repository takes this one command in its go.mod instead of Task's and go-tools' own: compiling
// those is about 450 modules and a minute or more on a runner, for programs their authors already
// release. This one links versions.yaml's reader and a download. It puts versions.yaml's `task`
// in the user's cache, held to its sha256, and go-tools' latest release, held to the sha256 its
// checksums give - every repository runs the newest commands main has passed its gate with, and
// none bumps them - each unpacked once (internal/toolcache). Then it becomes Task: the same
// process, so a signal, the terminal and the exit status are Task's.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spin-stack/go-tools/internal/toolcache"
	"github.com/spin-stack/go-tools/versions"
)

// goTools is where go-tools' releases are, made by its release.yml of main each time its gate
// passes.
const goTools = "https://github.com/spin-stack/go-tools"

func main() {
	err := run(context.Background(), os.Args[1:], goTools, syscall.Exec) // mutate-exempt: the process's own arguments; run is what the test drives
	fmt.Fprintln(os.Stderr, "task:", err)                                //nolint:errcheck // the error is the exit status's reason; stderr failing changes no status
	os.Exit(2)                                                           // mutate-exempt: main is reached only when the exec failed, which no test makes it do; run is what the tests drive
}

// execFunc replaces the process with argv[0]'s program: syscall.Exec, and a recorder in a test.
type execFunc func(path string, argv []string, env []string) error

// run finds the repository's versions.yaml, installs the Task it pins and the latest release of
// the tools at base, and execs Task with args. It returns only when that fails: on success the
// process is Task's.
func run(ctx context.Context, args []string, base string, exec execFunc) error {
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	file, err := find(wd)
	if err != nil {
		return err
	}
	v, err := versions.Load(file)
	if err != nil {
		return err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	root := filepath.Join(cache, "spin-stack", "tools")
	task, err := v.Get("task")
	if err != nil {
		return fmt.Errorf("%w: `go tool task` runs the Task it pins", err)
	}
	taskDir, err := toolcache.Install(ctx, task, root)
	if err != nil {
		return err
	}
	tools, err := toolcache.Latest(ctx, base, "go-tools", root)
	if err != nil {
		return err
	}
	bin := filepath.Join(taskDir, "task")
	path := tools + string(os.PathListSeparator) + os.Getenv("PATH")
	return exec(bin, append([]string{bin}, args...), withPath(os.Environ(), path))
}

// withPath is env with PATH set to path: replaced where it is, since a program that reads the
// environment from the front would find a second PATH appended after the first only by luck.
func withPath(env []string, path string) []string {
	var out []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, "PATH=") {
			out = append(out, kv)
		}
	}
	return append(out, "PATH="+path)
}

// find is versions.yaml in dir or the nearest directory above it, as Task finds its Taskfile.
func find(dir string) (string, error) {
	for {
		p := filepath.Join(dir, versions.File)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no versions.yaml here or above: `go tool task` runs the Task a repository's versions.yaml pins")
		}
		dir = parent
	}
}
