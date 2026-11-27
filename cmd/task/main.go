// Command task is Task at the version go-tools pins, with go-tools' commands on its PATH:
// `go tool task lint` runs the released Task, and a Taskfile says `mutate` where it would say
// `go tool mutate`.
//
//	go tool task [Task's arguments]
//
// A repository takes this one command in its go.mod instead of Task's and go-tools' own: compiling
// those is about 450 modules and a minute or more on a runner, for programs their authors already
// release. This one links versions.yaml's reader and a download. It puts the Task it carries
// (versions.yaml, embedded) in the user's cache, held to its sha256, and go-tools' latest release,
// held to the sha256 its checksums give - every repository runs the newest commands main has
// passed its gate with, and none bumps them - each unpacked once (internal/toolcache). Then it
// becomes Task: the same process, so a signal, the terminal and the exit status are Task's.
//
// A repository pins nothing for it: the Task is go-tools', for every platform a release is built
// for, and it moves with the go-tools version a go.mod requires: one pin, so no repository's copy
// lacks a platform another person works on. A repository whose versions.yaml has a `task` runs
// that one instead.
//
// The release also carries the tasks every repository has (internal/tasks' tasks.yml), and
// GO_TOOLS_TASKS names the unpacked copy: a Taskfile includes '{{.GO_TOOLS_TASKS}}' and has
// `versions`, `bump`, `release`, `lint:workflows` and `consumers` as every other repository has
// them, at the release its commands are from.
//
// The release it runs is never one before its own: this command is compiled from the module
// version a go.mod requires, which is a release's, and a cache that still names an earlier one as
// the latest is asked again. So a repository that takes something a release added - tasks.yml was
// one - requires that release in the same commit, and no machine runs the two apart.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"syscall"

	"github.com/spin-stack/go-tools/internal/release"
	"github.com/spin-stack/go-tools/internal/toolcache"
	"github.com/spin-stack/go-tools/versions"
)

// goTools is where go-tools' releases are, made by its release.yml of main each time its gate
// passes.
const goTools = "https://github.com/spin-stack/go-tools"

// tasksEnv is the variable that names the release's copy of tasks, the file internal/tasks holds.
const (
	tasksEnv = "GO_TOOLS_TASKS"
	tasks    = "tasks.yml"
)

//go:embed versions.yaml
var pins []byte

func main() {
	err := run(context.Background(), os.Args[1:], goTools, own(), ownTask(), os.Stderr, syscall.Exec) // mutate-exempt: the process's own arguments; run is what the test drives
	fmt.Fprintln(os.Stderr, "task:", err)                                                             //nolint:errcheck // the error is the exit status's reason; stderr failing changes no status
	os.Exit(2)                                                                                        // mutate-exempt: main is reached only when the exec failed, which no test makes it do; run is what the tests drive
}

// own is the module version this command was compiled from: the one a repository's go.mod
// requires, when `go tool task` built it there.
func own() string {
	info, _ := debug.ReadBuildInfo() // mutate-exempt: the build's own record, which a test's binary has none of; run is what the tests drive
	if info == nil {
		return ""
	}
	return info.Main.Version
}

// ownTask is the Task this command carries, which a test holds to the platforms a release is built
// for.
func ownTask() versions.Entry {
	v, err := versions.Parse(pins)
	if err != nil {
		panic(err)
	}
	e, err := v.Get("task")
	if err != nil {
		panic(err)
	}
	return e
}

// execFunc replaces the process with argv[0]'s program: syscall.Exec, and a recorder in a test.
type execFunc func(path string, argv []string, env []string) error

// run installs task - or the `task` of the repository's versions.yaml, when it has one - and the
// latest release of the tools at base - none before the release that version, this command's
// own, is the module version of - and execs Task with args. It returns only when that fails: on
// success the process is Task's. A release with no tasks.yml is said on warn and run all the same:
// only a Taskfile that includes it needs it, and whether this repository's does is Task's to read.
func run(ctx context.Context, args []string, base, version string, task versions.Entry, warn io.Writer, exec execFunc) error {
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	task, err = repositorys(wd, task)
	if err != nil {
		return err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	root := filepath.Join(cache, "spin-stack", "tools")
	taskDir, err := toolcache.Install(ctx, task, root)
	if err != nil {
		return err
	}
	tools, err := toolcache.Latest(ctx, base, "go-tools", root, release.Named(version))
	if err != nil {
		return err
	}
	common := filepath.Join(tools, tasks)
	if _, err := os.Stat(common); err != nil {
		//nolint:errcheck // said beside what Task says next; stderr failing changes nothing Task does
		fmt.Fprintf(warn, "task: the go-tools release this machine has (%s) has no %s: a Taskfile that includes {{.%s}} does not "+
			"load, and Task says so below. It is a release from before the tasks shipped. Which release is the latest is asked of "+
			"%s/releases/latest ten minutes after its last answer: run this again then, or when that address can be reached.\n",
			tools, tasks, tasksEnv, base)
	}
	bin := filepath.Join(taskDir, "task")
	path := tools + string(os.PathListSeparator) + os.Getenv("PATH")
	env := with(with(os.Environ(), "PATH", path), tasksEnv, common)
	return exec(bin, append([]string{bin}, args...), env)
}

// with is env with key set to value: replaced where it is, since a program that reads the
// environment from the front would find a second PATH appended after the first only by luck.
func with(env []string, key, value string) []string {
	var out []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return append(out, key+"="+value)
}

// repositorys is the Task to run from dir: the `task` of the versions.yaml in dir or the nearest
// directory above it, as Task finds its Taskfile, and own when there is none. A repository's entry
// is read whole: one that does not pin this platform is said, since running go-tools' Task in its
// place would run a version other than the one the file names.
func repositorys(dir string, own versions.Entry) (versions.Entry, error) {
	for {
		p := filepath.Join(dir, versions.File)
		if _, err := os.Stat(p); err == nil {
			v, err := versions.Load(p)
			if err != nil {
				return versions.Entry{}, err
			}
			i := slices.IndexFunc(v.Entries, func(e versions.Entry) bool { return e.Name == "task" })
			if i < 0 {
				return own, nil
			}
			if _, err := v.Entries[i].For(runtime.GOOS, runtime.GOARCH); err != nil {
				return versions.Entry{}, fmt.Errorf("%w: %s's task is the Task `go tool task` runs here. Without that entry go-tools' runs, which is pinned for every platform", err, p)
			}
			return v.Entries[i], nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return own, nil
		}
		dir = parent
	}
}
