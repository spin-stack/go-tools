package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// archive is a .tar.gz of one executable named name, and its sha256.
func archive(t *testing.T, name string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: 2, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x\n")); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(tw.Close(), gz.Close()); err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(s[:])
}

// repository is a directory whose versions.yaml pins task at a TLS server the default client
// trusts for the test, and the base URL of go-tools' releases there - its latest, v20261110.01,
// listed in its checksums. The user's cache is a directory of the test's.
func repository(t *testing.T) (repo, base string) {
	t.Helper()
	platform := runtime.GOOS + "_" + runtime.GOARCH
	taskArchive, taskPin := archive(t, "task")
	toolsArchive, toolsPin := archive(t, "mutate")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/task/v3.54.0/task_" + platform + ".tar.gz":
			_, _ = w.Write(taskArchive)
		case "/go-tools/releases/latest":
			http.Redirect(w, r, "/go-tools/releases/tag/v20261110.01", http.StatusFound)
		case "/go-tools/releases/download/v20261110.01/checksums.txt":
			_, _ = w.Write([]byte(toolsPin + "  go-tools_" + platform + ".tar.gz\n"))
		case "/go-tools/releases/download/v20261110.01/go-tools_" + platform + ".tar.gz":
			_, _ = w.Write(toolsArchive)
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
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	return repo, srv.URL + "/go-tools"
}

type call struct {
	path      string
	argv, env []string
	called    bool
}

func (c *call) exec(path string, argv, env []string) error {
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
	repo, base := repository(t)
	sub := filepath.Join(repo, "internal", "x")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	t.Setenv("PATH", "/usr/bin")
	var c call
	if err := run(t.Context(), []string{"lint", "--", "-v"}, base, c.exec); err != nil {
		t.Fatal(err)
	}
	if filepath.Base(c.path) != "task" || !slices.Equal(c.argv, []string{c.path, "lint", "--", "-v"}) {
		t.Errorf("exec'd %s %v", c.path, c.argv)
	}
	if st, err := os.Stat(c.path); err != nil || st.Mode().Perm()&0o100 == 0 {
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
}

// What `go tool task` cannot run is said, and nothing is exec'd: no versions.yaml above, one that
// pins no Task, one that does not read, no go-tools release to be had.
func TestWhatCannotRunTaskIsSaid(t *testing.T) {
	_, base := repository(t)
	for _, tc := range []struct {
		name, file, base, says string
	}{
		{"no versions.yaml", "", base, "no versions.yaml here or above"},
		{"no task in it", "- name: go\n  kind: date\n  version: \"1\"\n  track: today\n", base, "runs the Task it pins"},
		{"a file that does not read", "- name: [\n", base, "versions"},
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
			err := run(t.Context(), nil, tc.base, c.exec)
			if err == nil || !strings.Contains(err.Error(), tc.says) || c.called {
				t.Errorf("err = %v, exec'd %v; want %q", err, c.called, tc.says)
			}
		})
	}
	t.Run("no go-tools release", func(t *testing.T) {
		repo, base := repository(t)
		t.Chdir(repo)
		var c call
		err := run(t.Context(), nil, base+"/none", c.exec)
		if err == nil || !strings.Contains(err.Error(), "the latest release of") || c.called {
			t.Errorf("err = %v, exec'd %v", err, c.called)
		}
	})
}
