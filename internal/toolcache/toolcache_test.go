package toolcache

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	return releaseAs(t, body, ".tar.gz")
}

// releaseAs is release, its file named with ext.
func releaseAs(t *testing.T, body []byte, ext string) (string, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tool_linux_amd64"+ext {
			http.NotFound(w, r)
			return
		}
		n.Add(1)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v{version}/tool_{os}_{arch}" + ext, &n
}

// zipOf is a .zip of members, and its sha256.
func zipOf(t *testing.T, members ...member) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, m := range members {
		h := &zip.FileHeader{Name: m.name}
		h.SetMode(fs.FileMode(m.mode))
		if m.kind == tar.TypeDir {
			h.SetMode(fs.ModeDir | fs.FileMode(m.mode))
		}
		if m.kind == tar.TypeSymlink {
			h.SetMode(fs.ModeSymlink | 0o777)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(m.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(s[:])
}

// Upstream releases Windows' programs as .zip: one is unpacked as a .tar.gz is, its files and
// directories and no link, and one that would write outside its directory leaves nothing.
func TestAZipIsUnpackedAsATarballIs(t *testing.T) {
	body, pin := zipOf(t,
		member{name: "completion/", kind: tar.TypeDir, mode: 0o755},
		member{name: "task.exe", body: "MZ", mode: 0o755, kind: tar.TypeReg},
		member{name: "completion/task.ps1", body: "x", mode: 0o644, kind: tar.TypeReg},
		member{name: "link", body: "/etc/passwd", kind: tar.TypeSymlink})
	source, _ := releaseAs(t, body, ".zip")
	root := t.TempDir()
	dir, err := install(t.Context(), entry(source, pin), root, "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "task.exe")); err != nil || string(got) != "MZ" {
		t.Errorf("task.exe holds %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "completion", "task.ps1")); err != nil {
		t.Errorf("completion/task.ps1: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "link")); err == nil {
		t.Error("a link in the archive was made")
	}

	escape, escapePin := zipOf(t, member{name: "../outside", body: "x", mode: 0o644, kind: tar.TypeReg})
	escapeSource, _ := releaseAs(t, escape, ".zip")
	root = t.TempDir()
	if _, err := install(t.Context(), entry(escapeSource, escapePin), root, "linux", "amd64"); err == nil ||
		!strings.Contains(err.Error(), "would be written outside the directory") {
		t.Errorf("err = %v", err)
	}
	if left, _ := os.ReadDir(filepath.Join(root, "tool")); len(left) != 0 {
		t.Errorf("left in the cache: %v", left)
	}
}

// modes is whether this platform's files keep the permission bits they are written with: Windows
// has none, and says 0666 of every file.
var modes = runtime.GOOS != "windows"

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
	if err != nil || modes && st.Mode().Perm() != 0o755 {
		t.Errorf("bin/task: %v, %v", st, err)
	}
	if body, err := os.ReadFile(filepath.Join(dir, "bin", "task")); err != nil || string(body) != "#!/bin/sh\n" {
		t.Errorf("bin/task holds %q, %v", body, err)
	}
	if st, err := os.Stat(filepath.Join(dir, "LICENSE")); err != nil || modes && st.Mode().Perm() != 0o644 {
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
		{"no archive", versions.Entry{Name: "tool", Kind: versions.Download, Source: "http://x/{version}/tool.7z", Version: "1", Pin: pin},
			"only a .tar.gz or a .zip is unpacked"},
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

// github plays a repository's releases: /releases/latest redirects to *latest, each release in
// releases lists its archives in checksums.txt, and asked counts the questions for the latest.
type github struct {
	latest   *string
	releases map[string][]byte // tag -> linux/amd64 archive
	asked    atomic.Int32
	down     atomic.Bool
}

func (g *github) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/releases/latest" {
			g.asked.Add(1)
			http.Redirect(w, r, "/releases/tag/"+*g.latest, http.StatusFound)
			return
		}
		for tag, body := range g.releases {
			s := sha256.Sum256(body)
			switch r.URL.Path {
			case "/releases/download/" + tag + "/checksums.txt":
				_, _ = w.Write([]byte(hex.EncodeToString(s[:]) + "  go-tools_linux_amd64.tar.gz\n" + strings.Repeat("0", 64) + "  go-tools_darwin_arm64.tar.gz\n"))
				return
			case "/releases/download/" + tag + "/go-tools_linux_amd64.tar.gz":
				_, _ = w.Write(body)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// The latest release is installed held to the sha256 its checksums give, asked for at most every
// ten minutes, and a machine that cannot ask goes on with the last release it had.
func TestTheLatestReleaseIsAskedForAtMostEveryTenMinutes(t *testing.T) {
	one, _ := tarball(t, member{name: "mutate", body: "one", mode: 0o755, kind: tar.TypeReg})
	two, _ := tarball(t, member{name: "mutate", body: "two", mode: 0o755, kind: tar.TypeReg})
	tag := "v20261110.01"
	g := &github{latest: &tag, releases: map[string][]byte{"v20261110.01": one, "v20261110.02": two}}
	base := g.serve(t)
	root := t.TempDir()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	holds := func(at time.Time, want string) {
		t.Helper()
		dir, err := latest(t.Context(), base, "go-tools", root, "", "linux", "amd64", at, noWait)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "mutate")); string(got) != want {
			t.Errorf("at %s: mutate is %q, want %q", at.Format(time.TimeOnly), got, want)
		}
	}
	holds(now, "one")
	// A stamp is written at the machine's own time: it is the clock latest is given that is read
	// against it here, so the test moves that clock rather than the file's.
	stamp := filepath.Join(root, "go-tools", "latest")
	written := time.Now()
	tag = "v20261110.02"
	holds(written.Add(9*time.Minute), "one")
	if g.asked.Load() != 1 {
		t.Errorf("asked %d times within ten minutes", g.asked.Load())
	}
	holds(written.Add(11*time.Minute), "two")
	if g.asked.Load() != 2 {
		t.Errorf("asked %d times past ten minutes", g.asked.Load())
	}
	if got, _ := os.ReadFile(stamp); string(got) != "v20261110.02" {
		t.Errorf("the stamp says %q", got)
	}
	g.down.Store(true)
	holds(time.Now().Add(time.Hour), "two")
}

// What the latest release cannot be is said: GitHub down with nothing asked before, a redirect
// that is not to a release, a release whose checksums do not list this platform's archive.
func TestALatestReleaseThatCannotBeHadIsSaid(t *testing.T) {
	archive, _ := tarball(t, member{name: "mutate", body: "x", mode: 0o755, kind: tar.TypeReg})
	for _, tc := range []struct {
		name, tag, goos string
		down            bool
		says            string
	}{
		{"github down", "v20261110.01", "linux", true, "the latest release of"},
		{"no release", "", "linux", false, "not a release"},
		{"a release without it", "v20261110.01", "windows", false, "lists no go-tools_windows_amd64.tar.gz"},
		{"a release that is not there", "v20261110.09", "linux", false, "404"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tag := tc.tag
			g := &github{latest: &tag, releases: map[string][]byte{"v20261110.01": archive}}
			g.down.Store(tc.down)
			base := g.serve(t)
			if tc.name == "no release" {
				base += "/nothing"
			}
			_, err := latest(t.Context(), base, "go-tools", t.TempDir(), "", tc.goos, "amd64", time.Now(), noWait)
			if err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Errorf("err = %v, want %q", err, tc.says)
			}
		})
	}
}

// A caller that is of a release is proof of it: a name recorded moments ago that is before the
// caller's is asked again, where one that is the caller's or after it is taken for its ten
// minutes; and a machine that cannot ask goes on with the release it has, old as it is.
func TestAReleaseBeforeTheCallersIsNotTheLatestHoweverFresh(t *testing.T) {
	one, _ := tarball(t, member{name: "mutate", body: "one", mode: 0o755, kind: tar.TypeReg})
	two, _ := tarball(t, member{name: "mutate", body: "two", mode: 0o755, kind: tar.TypeReg})
	tag := "v20261110.01"
	g := &github{latest: &tag, releases: map[string][]byte{"v20261110.01": one, "v20261110.02": two}}
	base := g.serve(t)
	root := t.TempDir()
	holds := func(since, want string, asked int32) {
		t.Helper()
		dir, err := latest(t.Context(), base, "go-tools", root, since, "linux", "amd64", time.Now(), noWait)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "mutate")); string(got) != want || g.asked.Load() != asked {
			t.Errorf("a caller of %q: mutate is %q after %d questions, want %q after %d", since, got, g.asked.Load(), want, asked)
		}
	}
	holds("", "one", 1)
	holds("v20261110.01", "one", 1)
	holds("v20261109.07", "one", 1)

	// The release the caller is of is out, and the stamp is seconds old.
	tag = "v20261110.02"
	g.down.Store(true)
	holds("v20261110.02", "one", 1)
	g.down.Store(false)
	holds("v20261110.02", "two", 2)
	holds("v20261110.02", "two", 2)
}

// noWait is the wait of a test that asks GitHub once, or does not care how long it would have waited.
func noWait(context.Context, time.Duration) {}

// For a minute or two after a release GitHub answers some requests with the one before. To a
// caller of the new release that is no answer: it is asked again, after a wait each time, and the
// release run and recorded is the caller's own, never the one GitHub said first.
func TestAnAnswerBeforeTheCallersReleaseIsAskedAgain(t *testing.T) {
	one, _ := tarball(t, member{name: "mutate", body: "one", mode: 0o755, kind: tar.TypeReg})
	two, _ := tarball(t, member{name: "mutate", body: "two", mode: 0o755, kind: tar.TypeReg})
	tag := "v20261110.01"
	g := &github{latest: &tag, releases: map[string][]byte{"v20261110.01": one, "v20261110.02": two}}
	base := g.serve(t)
	root := t.TempDir()
	var waited []time.Duration
	// The old release twice, and the new one to the third question.
	wait := func(_ context.Context, d time.Duration) {
		if waited = append(waited, d); len(waited) == 2 {
			tag = "v20261110.02"
		}
	}
	dir, err := latest(t.Context(), base, "go-tools", root, "v20261110.02", "linux", "amd64", time.Now(), wait)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "mutate")); string(got) != "two" {
		t.Errorf("mutate is %q, the release before the caller's", got)
	}
	if g.asked.Load() != 3 || !slices.Equal(waited, []time.Duration{2 * time.Second, 4 * time.Second}) {
		t.Errorf("asked %d times, having waited %v", g.asked.Load(), waited)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "go-tools", "latest")); string(got) != "v20261110.02" {
		t.Errorf("the stamp says %q", got)
	}
}

// When GitHub goes on answering the release before the caller's, nothing is run: the command says
// its own release is not visible yet and to run it again, and records nothing, so the next run asks.
// A release already recorded makes no difference: it is before the caller's too.
func TestAnAnswerThatStaysBeforeTheCallersReleaseRunsNothing(t *testing.T) {
	one, _ := tarball(t, member{name: "mutate", body: "one", mode: 0o755, kind: tar.TypeReg})
	tag := "v20261110.01"
	g := &github{latest: &tag, releases: map[string][]byte{"v20261110.01": one}}
	base := g.serve(t)
	for _, recorded := range []string{"", "v20261110.01"} {
		root := t.TempDir()
		stamp := filepath.Join(root, "go-tools", "latest")
		if recorded != "" {
			if _, err := latest(t.Context(), base, "go-tools", root, "", "linux", "amd64", time.Now(), noWait); err != nil {
				t.Fatal(err)
			}
		}
		g.asked.Store(0)
		var waited time.Duration
		dir, err := latest(t.Context(), base, "go-tools", root, "v20261110.02", "linux", "amd64", time.Now(),
			func(_ context.Context, d time.Duration) { waited += d })
		if err == nil {
			t.Fatalf("with %q recorded: ran %s, a release before the caller's", recorded, dir)
		}
		for _, says := range []string{"v20261110.02, the release this command is of, is not visible yet", "still answers v20261110.01", "run it again"} {
			if !strings.Contains(err.Error(), says) {
				t.Errorf("with %q recorded: %v, which does not say %q", recorded, err, says)
			}
		}
		if g.asked.Load() != 4 || waited != 14*time.Second {
			t.Errorf("with %q recorded: asked %d times over %s", recorded, g.asked.Load(), waited)
		}
		if got, _ := os.ReadFile(stamp); string(got) != recorded {
			t.Errorf("the stamp says %q, and %q was recorded before", got, recorded)
		}
	}
}

// The wait between two questions is the time asked for, cut short when the command is.
func TestTheWaitEndsWithTheCommand(t *testing.T) {
	start := time.Now()
	sleep(t.Context(), 30*time.Millisecond)
	if took := time.Since(start); took < 30*time.Millisecond {
		t.Errorf("waited %s of 30ms", took)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start = time.Now()
	sleep(ctx, time.Minute)
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("a cancelled wait took %s", took)
	}
}

// A stamp that says nothing is no release, however fresh: the latest is asked for.
func TestAnEmptyStampIsAskedPast(t *testing.T) {
	archive, _ := tarball(t, member{name: "mutate", body: "x", mode: 0o755, kind: tar.TypeReg})
	tag := "v20261110.01"
	g := &github{latest: &tag, releases: map[string][]byte{tag: archive}}
	base := g.serve(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "go-tools"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go-tools", "latest"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := latest(t.Context(), base, "go-tools", root, "", "linux", "amd64", time.Now(), noWait); err != nil || g.asked.Load() != 1 {
		t.Errorf("err = %v, asked %d times", err, g.asked.Load())
	}
}

// Only a redirect to a release is the latest release: one elsewhere, one to a release with no
// name, and a release's address in an answer that is no redirect are not.
func TestOnlyARedirectToAReleaseIsTheLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/elsewhere/releases/latest":
			http.Redirect(w, r, "/elsewhere/login", http.StatusFound)
		case "/unnamed/releases/latest":
			http.Redirect(w, r, "/unnamed/releases/tag/", http.StatusFound)
		case "/not-a-redirect/releases/latest":
			w.Header().Set("Location", "/not-a-redirect/releases/tag/v20261110.01")
			w.WriteHeader(http.StatusOK)
		case "/good/releases/latest":
			http.Redirect(w, r, "/good/releases/tag/v20261110.01", http.StatusFound)
		}
	}))
	t.Cleanup(srv.Close)
	for _, repo := range []string{"elsewhere", "unnamed", "not-a-redirect"} {
		if tag, err := askLatest(t.Context(), srv.URL+"/"+repo); err == nil {
			t.Errorf("%s: %q is the latest release", repo, tag)
		}
	}
	if tag, err := askLatest(t.Context(), srv.URL+"/good"); err != nil || tag != "v20261110.01" {
		t.Errorf("good: %q, %v", tag, err)
	}
}

// githubHosts plays github.com redirecting o/r's /releases/latest to redirect, or answering 504
// when it is "", and GitHub's API naming *latest as o/r's latest release, through
// http.DefaultClient's transport, so the hosts are GitHub's own. Each request to the API is
// written down by its Authorization header.
func githubHosts(t *testing.T, redirect string, latest *string) *[]string {
	var api []string
	before := http.DefaultClient.Transport
	http.DefaultClient.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		switch r.URL.Host + r.URL.Path {
		case "github.com/o/r/releases/latest":
			if redirect == "" {
				http.Error(rec, "", http.StatusGatewayTimeout)
				break
			}
			http.Redirect(rec, r, "/o/r/releases/tag/"+redirect, http.StatusFound)
		case "api.github.com/repos/o/r/releases/latest":
			api = append(api, r.Header.Get("Authorization"))
			_, _ = rec.WriteString(`{"tag_name":"` + *latest + `"}`)
		default:
			http.NotFound(rec, r)
		}
		resp := rec.Result()
		resp.Request = r
		return resp, nil
	})
	t.Cleanup(func() { http.DefaultClient.Transport = before })
	return &api
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// When github.com answers 504 to /releases/latest, the latest release is the one GitHub's API
// names, asked with GITHUB_TOKEN when there is one, and recorded for its ten minutes.
func TestTheLatestReleaseIsAskedOfTheAPIWhenTheRedirectFails(t *testing.T) {
	for _, tok := range []string{"", "tok"} {
		t.Setenv("GITHUB_TOKEN", tok)
		t.Setenv("GH_TOKEN", "")
		tag := "v20261127.13"
		api := githubHosts(t, "", &tag)
		stamp := filepath.Join(t.TempDir(), "go-tools", "latest")
		got, err := latestTag(t.Context(), "https://github.com/o/r", stamp, "", time.Now(), noWait)
		if err != nil || got != tag {
			t.Fatalf("with %q: %q, %v", tok, got, err)
		}
		want := ""
		if tok != "" {
			want = "Bearer " + tok
		}
		if !slices.Equal(*api, []string{want}) {
			t.Errorf("with %q: the API was asked with %q", tok, *api)
		}
		if rec, _ := os.ReadFile(stamp); string(rec) != tag {
			t.Errorf("with %q: the stamp says %q", tok, rec)
		}
	}
}

// While the redirect answers, the API is not asked, whatever it would say: its quota is kept for
// when the redirect fails.
func TestTheAPIIsNotAskedWhileTheRedirectAnswers(t *testing.T) {
	tag := "v20261127.12"
	api := githubHosts(t, "v20261127.13", &tag)
	got, err := latestTag(t.Context(), "https://github.com/o/r", filepath.Join(t.TempDir(), "latest"), "", time.Now(), noWait)
	if err != nil || got != "v20261127.13" || len(*api) != 0 {
		t.Errorf("%q, %v, the API asked %d times", got, err, len(*api))
	}
}

// The API's answer is held to the caller's release as the redirect's is: one before it is asked
// again, and when it stays nothing is run and nothing recorded.
func TestAnAPIAnswerBeforeTheCallersReleaseRunsNothing(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	tag := "v20261127.12"
	api := githubHosts(t, "", &tag)
	stamp := filepath.Join(t.TempDir(), "go-tools", "latest")
	_, err := latestTag(t.Context(), "https://github.com/o/r", stamp, "v20261127.13", time.Now(), noWait)
	if err == nil || !strings.Contains(err.Error(), "v20261127.13, the release this command is of, is not visible yet") {
		t.Errorf("err = %v", err)
	}
	if len(*api) != 4 {
		t.Errorf("the API was asked %d times", len(*api))
	}
	if _, err := os.Stat(stamp); err == nil {
		t.Error("a release before the caller's was recorded")
	}
}
