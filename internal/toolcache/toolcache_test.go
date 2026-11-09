package toolcache

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spin-stack/go-tools/versions"
)

type member struct {
	name, body string
	mode       int64
	kind       byte
}

// tarball is a gzipped tar of members, and its sha256.
func tarball(t *testing.T, members ...member) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, m := range members {
		h := &tar.Header{Name: m.name, Mode: m.mode, Size: int64(len(m.body)), Typeflag: m.kind}
		if m.kind != tar.TypeReg {
			h.Size = 0
			h.Linkname = m.body
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if m.kind == tar.TypeReg {
			if _, err := tw.Write([]byte(m.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(s[:])
}

// release serves body at /v1/tool_<os>_<arch>.tar.gz and counts the downloads.
func release(t *testing.T, body []byte) (string, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tool_linux_amd64.tar.gz" {
			http.NotFound(w, r)
			return
		}
		n.Add(1)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v{version}/tool_{os}_{arch}.tar.gz", &n
}

func entry(source, pin string) versions.Entry {
	return versions.Entry{Name: "tool", Kind: versions.Download, Version: "1", Source: source,
		Pins: map[string]string{"linux/amd64": pin}}
}

// A release is unpacked once, by its pin: its programs keep their modes, a second install is the
// directory already there, and the archive is not kept beside it.
func TestAReleaseIsUnpackedOnceByItsPin(t *testing.T) {
	body, pin := tarball(t,
		member{name: "bin/", kind: tar.TypeDir, mode: 0o755},
		member{name: "bin/task", body: "#!/bin/sh\n", mode: 0o755, kind: tar.TypeReg},
		member{name: "LICENSE", body: "MIT", mode: 0o644, kind: tar.TypeReg},
		member{name: "bin/link", body: "/etc/passwd", kind: tar.TypeSymlink})
	source, downloads := release(t, body)
	root := t.TempDir()
	dir, err := install(t.Context(), entry(source, pin), root, "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(root, "tool", pin) {
		t.Errorf("installed in %s", dir)
	}
	st, err := os.Stat(filepath.Join(dir, "bin", "task"))
	if err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("bin/task: %v, %v", st, err)
	}
	if body, err := os.ReadFile(filepath.Join(dir, "bin", "task")); err != nil || string(body) != "#!/bin/sh\n" {
		t.Errorf("bin/task holds %q, %v", body, err)
	}
	if st, err := os.Stat(filepath.Join(dir, "LICENSE")); err != nil || st.Mode().Perm() != 0o644 {
		t.Errorf("LICENSE: %v, %v", st, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "bin", "link")); err == nil {
		t.Error("a link in the archive was made")
	}
	if again, err := install(t.Context(), entry(source, pin), root, "linux", "amd64"); err != nil || again != dir {
		t.Errorf("again: %s, %v", again, err)
	}
	if downloads.Load() != 1 {
		t.Errorf("downloaded %d times", downloads.Load())
	}
	left, _ := os.ReadDir(filepath.Join(root, "tool"))
	if len(left) != 1 {
		t.Errorf("beside the directory: %v", left)
	}
}

// What is not the release versions.yaml pins, or would write outside its directory, leaves nothing
// in the cache; neither does an entry that is no archive of this platform.
func TestWhatIsNotThePinnedReleaseLeavesNothing(t *testing.T) {
	good, pin := tarball(t, member{name: "task", body: "x", mode: 0o755, kind: tar.TypeReg})
	escape, escapePin := tarball(t, member{name: "../outside", body: "x", mode: 0o644, kind: tar.TypeReg})
	notGzip := []byte("not an archive")
	sumOf := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	goodSource, _ := release(t, good)
	escapeSource, _ := release(t, escape)
	notGzipSource, _ := release(t, notGzip)
	for _, tc := range []struct {
		name string
		e    versions.Entry
		says string
	}{
		{"another sha256", entry(goodSource, strings.Repeat("0", 64)), "is not what versions.yaml pins"},
		{"a name outside the directory", entry(escapeSource, escapePin), "would be written outside the directory"},
		{"no gzip", entry(notGzipSource, sumOf(notGzip)), "gzip"},
		{"a platform not pinned", versions.Entry{Name: "tool", Kind: versions.Download, Source: goodSource, Version: "1",
			Pins: map[string]string{"darwin/arm64": pin}}, "pins no file for linux/amd64"},
		{"no archive", versions.Entry{Name: "tool", Kind: versions.Download, Source: "http://x/{version}/tool.zip", Version: "1", Pin: pin},
			"only a .tar.gz is unpacked"},
		{"no download", versions.Entry{Name: "tool", Kind: versions.Git, Version: "1"}, "not a download"},
		{"nothing there", entry(strings.Replace(goodSource, "/v{version}/", "/gone-{version}/", 1), pin), "404"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			_, err := install(t.Context(), tc.e, root, "linux", "amd64")
			if err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("err = %v, want %q", err, tc.says)
			}
			left, _ := os.ReadDir(filepath.Join(root, "tool"))
			if len(left) != 0 {
				t.Errorf("left in the cache: %v", left)
			}
			if _, err := os.Stat(filepath.Join(root, "outside")); err == nil {
				t.Error("a file was written outside the directory")
			}
		})
	}
}

// Install is this platform's: on another, the pin it reads is another's.
func TestInstallIsThisPlatforms(t *testing.T) {
	e := versions.Entry{Name: "tool", Kind: versions.Download, Source: "http://x/{version}/tool_{os}_{arch}.tar.gz", Version: "1",
		Pins: map[string]string{"plan9/mips": strings.Repeat("0", 64)}}
	if _, err := Install(t.Context(), e, t.TempDir()); err == nil || !strings.Contains(err.Error(), "pins no file for") {
		t.Errorf("err = %v", err)
	}
}

// Two processes installing the same pin at once are both right: the one that renames second finds
// the other's directory in place, and answers it.
func TestAnInstallThatLosesTheRaceAnswersTheWinnersDirectory(t *testing.T) {
	body, pin := tarball(t, member{name: "task", body: "mine", mode: 0o755, kind: tar.TypeReg})
	root := t.TempDir()
	winner := filepath.Join(root, "tool", pin)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// While this one downloads, another puts the same pin in place.
		if err := os.MkdirAll(winner, 0o750); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(filepath.Join(winner, "task"), []byte("theirs"), 0o600); err != nil {
			t.Error(err)
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	dir, err := install(t.Context(), entry(srv.URL+"/v{version}/tool_{os}_{arch}.tar.gz", pin), root, "linux", "amd64")
	if err != nil || dir != winner {
		t.Fatalf("installed %s, %v", dir, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "task")); string(got) != "theirs" {
		t.Errorf("the winner's directory holds %q", got)
	}
	if left, _ := os.ReadDir(filepath.Join(root, "tool")); len(left) != 1 {
		t.Errorf("beside it: %v", left)
	}
}
