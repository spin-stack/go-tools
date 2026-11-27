package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/spin-stack/go-tools/versions"
)

// archive is a .tar.gz of an executable for each of names, and its sha256.
func archive(t *testing.T, names ...string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range names {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: 2, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("x\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(tw.Close(), gz.Close()); err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(s[:])
}

// repository is a directory whose versions.yaml pins task at a TLS server the default client
// trusts for the test, the base URL of go-tools' releases there - its latest, v20261110.01,
// listed in its checksums - and the Task go-tools carries, another file there. The user's cache is
// a directory of the test's.
func repository(t *testing.T) (repo, base string, own versions.Entry) {
	t.Helper()
	latest := before
	return repositoryAt(t, &latest)
}

// The releases repositoryAt serves: one from before a release carried tasks.yml, and one with it.
const (
	before = "v20261110.01"
	after  = "v20261122.04"
)

// repositoryAt is repository with go-tools' latest release the one *latest names when asked.
func repositoryAt(t *testing.T, latest *string) (repo, base string, own versions.Entry) {
	t.Helper()
	platform := runtime.GOOS + "_" + runtime.GOARCH
	program := "task" + exe(runtime.GOOS)
	taskArchive, taskPin := archive(t, program)
	ownArchive, ownPin := archive(t, program, "LICENSE")
	toolsArchive, toolsPin := archive(t, "mutate")
	withTasks, withTasksPin := archive(t, "mutate", "tasks.yml")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/task/v3.54.0/task_" + platform + ".tar.gz":
			_, _ = w.Write(taskArchive)
		case "/task/v3.99.0/task_" + platform + ".tar.gz":
			_, _ = w.Write(ownArchive)
		case "/go-tools/releases/latest":
			http.Redirect(w, r, "/go-tools/releases/tag/"+*latest, http.StatusFound)
		case "/go-tools/releases/download/" + before + "/checksums.txt":
			_, _ = w.Write([]byte(toolsPin + "  go-tools_" + platform + ".tar.gz\n"))
		case "/go-tools/releases/download/" + before + "/go-tools_" + platform + ".tar.gz":
			_, _ = w.Write(toolsArchive)
		case "/go-tools/releases/download/" + after + "/checksums.txt":
			_, _ = w.Write([]byte(withTasksPin + "  go-tools_" + platform + ".tar.gz\n"))
		case "/go-tools/releases/download/" + after + "/go-tools_" + platform + ".tar.gz":
			_, _ = w.Write(withTasks)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	before := http.DefaultClient.Transport
	http.DefaultClient.Transport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultClient.Transport = before })

	file := `- name: task
  kind: download
  source: ` + srv.URL + `/task/v{version}/task_{os}_{arch}.tar.gz
  version: 3.54.0
  pins:
    ` + runtime.GOOS + "/" + runtime.GOARCH + `: ` + taskPin + `
  track: github-release https://github.com/go-task/task
`
	repo = t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "versions.yaml"), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	// The user's cache is XDG_CACHE_HOME's on Linux, under HOME on macOS and LocalAppData's on
	// Windows: each test has one of its own, or a release one installed is the next one's.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LocalAppData", t.TempDir())
	carried, err := versions.Parse([]byte(strings.NewReplacer("3.54.0", "3.99.0", taskPin, ownPin).Replace(file)))
	if err != nil {
		t.Fatal(err)
	}
	own, err = carried.Get("task")
	if err != nil {
		t.Fatal(err)
	}
	return repo, srv.URL + "/go-tools", own
}

// ran is the Task run would exec from dir, and its error.
func ran(t *testing.T, dir, base string, own versions.Entry) (string, error) {
	t.Helper()
	t.Chdir(dir)
	var c call
	err := run(t.Context(), nil, base, "", own, io.Discard, c.exec)
	if err == nil && !c.called {
		t.Fatal("run returned without exec'ing Task or an error")
	}
	return c.path, err
}

type call struct {
	path      string
	argv, env []string
	called    bool
}

func (c *call) exec(_ context.Context, path string, argv, env []string) error {
	c.path, c.argv, c.env, c.called = path, argv, env, true
	return nil
}

func pathOf(env []string) []string {
	var paths []string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			paths = append(paths, v)
		}
	}
	return paths
}

// From anywhere under the repository, `go tool task` is the pinned Task, given the arguments it
// was given, with the latest go-tools first on a PATH that is there once.
func TestTaskIsThePinnedTaskWithTheLatestToolsOnItsPath(t *testing.T) {
	latest := after
	repo, base, own := repositoryAt(t, &latest)
	sub := filepath.Join(repo, "internal", "x")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("GO_TOOLS_TASKS", "/a/release/before")
	var c call
	var warned strings.Builder
	if err := run(t.Context(), []string{"lint", "--", "-v"}, base, "", own, &warned, c.exec); err != nil {
		t.Fatal(err)
	}
	if warned.Len() != 0 {
		t.Errorf("a release with its tasks was warned of: %s", &warned)
	}
	if filepath.Base(c.path) != "task"+exe(runtime.GOOS) || !slices.Equal(c.argv, []string{c.path, "lint", "--", "-v"}) {
		t.Errorf("exec'd %s %v", c.path, c.argv)
	}
	// Windows runs a file by its name, not by a mode it has none of.
	if st, err := os.Stat(c.path); err != nil || runtime.GOOS != "windows" && st.Mode().Perm()&0o100 == 0 {
		t.Errorf("Task is not an executable there: %v, %v", st, err)
	}
	paths := pathOf(c.env)
	if len(paths) != 1 {
		t.Fatalf("PATH is in the environment %d times: %v", len(paths), paths)
	}
	first, rest, _ := strings.Cut(paths[0], string(os.PathListSeparator))
	if _, err := os.Stat(filepath.Join(first, "mutate")); err != nil || rest != "/usr/bin" {
		t.Errorf("PATH = %s: go-tools' directory is not first, or the rest is not the caller's", paths[0])
	}
	if named := tasksOf(c.env); !slices.Equal(named, []string{filepath.Join(first, "tasks.yml")}) {
		t.Errorf("GO_TOOLS_TASKS = %v, want once and the tasks.yml of the release on PATH, %s", named, first)
	}
}

// tasksOf is every GO_TOOLS_TASKS in env.
func tasksOf(env []string) []string {
	var named []string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GO_TOOLS_TASKS="); ok {
			named = append(named, v)
		}
	}
	return named
}

// A cache that named the latest release moments before a new one came out still names the old
// one, which has no tasks.yml. A command compiled from the new release's module version does not
// run that one: it asks again and runs its own release, with the file. A command that is of no
// release, or of the old one, runs the one the cache names, says what is missing and what to do,
// and runs Task all the same.
func TestAReleaseFromBeforeThisCommandIsNotRunForBeingFresh(t *testing.T) {
	latest := before
	repo, base, own := repositoryAt(t, &latest)
	t.Chdir(repo)
	ran := func(version string) (tasks string, warned string) {
		t.Helper()
		var c call
		var w strings.Builder
		if err := run(t.Context(), nil, base, version, own, &w, c.exec); err != nil || !c.called {
			t.Fatalf("a command of %q: %v, exec'd %t", version, err, c.called)
		}
		named := tasksOf(c.env)
		if len(named) != 1 {
			t.Fatalf("GO_TOOLS_TASKS = %v", named)
		}
		return named[0], w.String()
	}
	missing := func(version string) {
		t.Helper()
		tasks, warned := ran(version)
		if _, err := os.Stat(tasks); err == nil {
			t.Errorf("a command of %q runs a release with tasks.yml, and the cache's has none", version)
		}
		for _, says := range []string{"has no tasks.yml", filepath.Dir(tasks), "{{.GO_TOOLS_TASKS}}", base + "/releases/latest", "run this again then"} {
			if !strings.Contains(warned, says) {
				t.Errorf("a command of %q, of a release with no tasks.yml, does not say %q:\n%s", version, says, warned)
			}
		}
	}
	missing("")

	latest = after
	missing("")
	missing("(devel)")
	missing("v0.20261110.1")

	tasks, warned := ran("v0.20261122.4")
	if _, err := os.Stat(tasks); err != nil || warned != "" {
		t.Errorf("a command of the new release runs %s (%v), and said: %q", tasks, err, warned)
	}
}

// The Task is go-tools' where nothing names another: no versions.yaml above, or one without a
// task. A repository's task, where it has one, is the one run, from anywhere under it.
func TestTaskIsGoToolsUnlessTheRepositoryPinsOne(t *testing.T) {
	repo, base, own := repository(t)
	pinOf := func(path string) string { return filepath.Base(filepath.Dir(path)) }
	sub := filepath.Join(repo, "internal", "x")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{repo, sub} {
		if got, err := ran(t, dir, base, own); err != nil || pinOf(got) == own.Pins[runtime.GOOS+"/"+runtime.GOARCH] {
			t.Errorf("from %s, in a repository that pins task, ran %s (%v): go-tools' Task", dir, got, err)
		}
	}
	for name, file := range map[string]string{
		"no versions.yaml": "",
		"no task in it":    "- name: go\n  kind: date\n  version: \"1\"\n  track: today\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if file != "" {
				if err := os.WriteFile(filepath.Join(dir, "versions.yaml"), []byte(file), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := ran(t, dir, base, own); err != nil || pinOf(got) != own.Pins[runtime.GOOS+"/"+runtime.GOARCH] {
				t.Errorf("ran %s (%v), not go-tools' Task", got, err)
			}
		})
	}
}

// go-tools' Task is pinned for each platform a release is built for: a release on a platform
// without it would unpack commands no `go tool task` there can run.
func TestTheTaskCarriedIsForEveryReleasedPlatform(t *testing.T) {
	taskfile, err := os.ReadFile("../../Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	var released []string
	for line := range strings.Lines(string(taskfile)) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "PLATFORMS:"); ok {
			released = strings.Fields(v)
		}
	}
	if len(released) == 0 {
		t.Fatal("the Taskfile names no release PLATFORMS")
	}
	for _, p := range released {
		goos, goarch, _ := strings.Cut(p, "/")
		e, err := ownTask(goos, goarch).For(goos, goarch)
		if err != nil {
			t.Errorf("a release is built for %s: %v", p, err)
			continue
		}
		if !strings.HasSuffix(e.URL(), ".tar.gz") && !strings.HasSuffix(e.URL(), ".zip") {
			t.Errorf("%s's Task is %s, which is no archive go tool task unpacks", p, e.URL())
		}
	}
}

// What `go tool task` cannot run is said, and nothing is exec'd: a versions.yaml that does not
// read, one whose task is not for this platform, no go-tools release to be had.
func TestWhatCannotRunTaskIsSaid(t *testing.T) {
	_, base, own := repository(t)
	other := "windows/amd64"
	if runtime.GOOS == "windows" {
		other = "linux/amd64"
	}
	for _, tc := range []struct {
		name, file, base, says string
	}{
		{"a file that does not read", "- name: [\n", base, "versions"},
		{"a task not for this platform", "- name: task\n  kind: download\n  source: https://example.com/v{version}/task_{os}_{arch}.tar.gz\n  version: 3.54.0\n  pins:\n    " + other + ": " + strings.Repeat("a", 64) + "\n  track: github-release https://github.com/go-task/task\n", base, "Without that entry go-tools' runs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.file != "" {
				if err := os.WriteFile(filepath.Join(dir, "versions.yaml"), []byte(tc.file), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(dir)
			var c call
			err := run(t.Context(), nil, tc.base, "", own, io.Discard, c.exec)
			if err == nil || !strings.Contains(err.Error(), tc.says) || c.called {
				t.Errorf("err = %v, exec'd %v; want %q", err, c.called, tc.says)
			}
		})
	}
	t.Run("no go-tools release", func(t *testing.T) {
		repo, base, own := repository(t)
		t.Chdir(repo)
		var c call
		err := run(t.Context(), nil, base+"/none", "", own, io.Discard, c.exec)
		if err == nil || !strings.Contains(err.Error(), "the latest release of") || c.called {
			t.Errorf("err = %v, exec'd %v", err, c.called)
		}
	})
}

// A program's file on Windows is named with .exe, which is the name Task's .zip has it under, and
// on every other platform with nothing.
func TestAProgramIsNamedAsItsPlatformNamesIt(t *testing.T) {
	for goos, want := range map[string]string{"windows": ".exe", "linux": "", "darwin": ""} {
		if got := exe(goos); got != want {
			t.Errorf("exe(%q) = %q, want %q", goos, got, want)
		}
	}
}

// On a platform nothing pins, go tool task carries task itself, which says what is missing when it
// is installed rather than failing before it can.
func TestAPlatformNothingPinsIsSaidByTask(t *testing.T) {
	e := ownTask("plan9", "amd64")
	if _, err := e.For("plan9", "amd64"); e.Name != "task" || err == nil || !strings.Contains(err.Error(), "pins no file for plan9/amd64") {
		t.Errorf("ownTask is %s, and For says %v", e.Name, err)
	}
}
