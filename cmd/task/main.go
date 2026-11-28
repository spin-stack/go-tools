// Command task is Task at the version go-tools pins, with go-tools' commands on its PATH:
// `task lint` runs the released Task, and a Taskfile says `mutate` where it would say
// `go tool mutate`.
//
//	task [Task's arguments]
//
// It is installed outside any repository - the action at go-tools' root in a workflow, `go
// install github.com/spin-stack/go-tools/cmd/task@latest` on a laptop - so no go.mod pins it and
// no release of go-tools is a pull request anywhere. It puts the Task it carries (versions.yaml,
// embedded) in the user's cache, held to its sha256, and go-tools' latest release, held to the
// sha256 its checksums give, each unpacked once (internal/toolcache). The release carries its own
// build of this command, and an older one runs that in its place (forward): installed once, it
// runs the latest release's Task and commands from then on. Then it becomes Task: the same
// process, so a signal, the terminal and the exit status are Task's.
//
// The Task is go-tools', for every platform a release is built for, so no repository's copy lacks
// a platform another person works on. A repository whose versions.yaml has a `task` runs that one
// instead.
//
// The release also carries the tasks every repository has (internal/tasks' tasks.yml), and
// GO_TOOLS_TASKS names the unpacked copy: a Taskfile includes '{{.GO_TOOLS_TASKS}}' and has
// `versions`, `bump`, `release`, `lint:workflows` and `consumers` as every other repository has
// them, at the release its commands are from.
//
// The release it runs is never one before its own, when it was built as a module version: a cache
// that still names an earlier one as the latest is asked again.
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

	"github.com/spin-stack/go-tools/internal/interrupt"
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
	self, _ := os.Executable()                                                                                                     // mutate-exempt: "" when unknown, which only forgoes the forward; run is what the tests drive
	err := run(interrupt.Context(), os.Args[1:], goTools, own(), self, ownTask(runtime.GOOS, runtime.GOARCH), os.Stderr, execTask) // mutate-exempt: the process's own arguments; run is what the test drives
	fmt.Fprintln(os.Stderr, "task:", err)                                                                                          //nolint:errcheck // the error is the exit status's reason; stderr failing changes no status
	os.Exit(2)                                                                                                                     // mutate-exempt: main is reached only when the exec failed, which no test makes it do; run is what the tests drive
}

// shim is the directory of a release that holds its build of this command: not the one on Task's
// PATH, where a second task would stand in front of Task.
const shim = "shim"

// own is the module version this command was compiled from, when `go install` or a go.mod built it.
func own() string {
	info, _ := debug.ReadBuildInfo() // mutate-exempt: the build's own record, which a test's binary has none of; run is what the tests drive
	if info == nil {
		return ""
	}
	return info.Main.Version
}

// ownTask is the Task this command carries for goos/goarch: the entry of versions.yaml that pins
// that platform, task itself when none does, which says so when it is installed. A test holds
// the file to the platforms a release is built for.
func ownTask(goos, goarch string) versions.Entry {
	v, err := versions.Parse(pins)
	if err != nil {
		panic(err)
	}
	for _, e := range v.Entries {
		if _, ok := e.Pins[goos+"/"+goarch]; ok {
			return e
		}
	}
	e, err := v.Get("task")
	if err != nil {
		panic(err)
	}
	return e
}

// execFunc becomes argv[0]'s program: execTask, and a recorder in a test.
type execFunc func(ctx context.Context, path string, argv []string, env []string) error

// run installs the latest release of the tools at base - none before the release that version,
// this command's own, is the module version of - and task, or the `task` of the repository's
// versions.yaml when it has one, and execs Task with args. When the release's own build of this
// command is not self, the file this process runs, that build is exec'd with args instead. It
// returns only when the exec fails: on success the process is Task's. A release with no tasks.yml
// is said on warn and run all the same: only a Taskfile that includes it needs it, and whether
// this repository's does is Task's to read.
func run(ctx context.Context, args []string, base, version, self string, task versions.Entry, warn io.Writer, exec execFunc) error {
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	root := filepath.Join(cache, "spin-stack", "tools")
	tools, err := toolcache.Latest(ctx, base, "go-tools", root, release.Named(version))
	if err != nil {
		return err
	}
	if next := filepath.Join(tools, shim, "task"+exe(runtime.GOOS)); forward(self, next) {
		return exec(ctx, next, append([]string{next}, args...), os.Environ())
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	task, err = repositorys(wd, task)
	if err != nil {
		return err
	}
	taskDir, err := toolcache.Install(ctx, task, root)
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
	bin := filepath.Join(taskDir, "task"+exe(runtime.GOOS))
	path := tools + string(os.PathListSeparator) + os.Getenv("PATH")
	env := with(with(os.Environ(), "PATH", path), tasksEnv, common)
	return exec(ctx, bin, append([]string{bin}, args...), env)
}

// forward is whether next, the latest release's build of this command, runs in place of self: it
// carries that release's Task, which a command installed months ago does not. Not when self is
// next, which ends the chain, nor when self is unknown, nor when the release has no build.
func forward(self, next string) bool {
	n, err := os.Stat(next)
	if self == "" || err != nil {
		return false
	}
	s, err := os.Stat(self)
	return err == nil && !os.SameFile(s, n)
}

// exe is what a program's file name ends in on goos.
func exe(goos string) string {
	if goos == "windows" {
		return ".exe"
	}
	return ""
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
				return versions.Entry{}, fmt.Errorf("%w: %s's task is the Task `task` runs here. Without that entry go-tools' runs, which is pinned for every platform", err, p)
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
