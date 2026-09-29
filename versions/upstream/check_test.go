package upstream

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spin-stack/go-tools/versions"
)

// gitRepo is a repository with a lightweight tag v1.0.0 on its first commit, an annotated tag
// v1.2.0 and a branch topic on its second, and master one commit past them.
type gitRepo struct {
	dir, first, second, head string
}

func newGitRepo(t *testing.T) gitRepo {
	t.Helper()
	r := gitRepo{dir: t.TempDir()}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", r.dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "master")
	git("commit", "-q", "--allow-empty", "-m", "one")
	r.first = git("rev-parse", "HEAD")
	git("tag", "v1.0.0")
	git("commit", "-q", "--allow-empty", "-m", "two")
	r.second = git("rev-parse", "HEAD")
	git("tag", "-a", "-m", "annotated", "v1.2.0")
	git("branch", "topic")
	git("commit", "-q", "--allow-empty", "-m", "three")
	r.head = git("rev-parse", "HEAD")
	return r
}

// A git pin is a commit: an annotated tag's is the commit it points at, not the tag's own object,
// and a branch's is its head.
func TestAGitPinIsTheCommit(t *testing.T) {
	r := newGitRepo(t)
	e := versions.Entry{Name: "x", Kind: versions.Git, Source: r.dir}
	for version, want := range map[string]string{"v1.0.0": r.first, "v1.2.0": r.second, "topic": r.second, "master": r.head} {
		if got, err := resolve(t.Context(), e, version); err != nil || got != want {
			t.Errorf("%s: pin %q, %v; want %q", version, got, err, want)
		}
	}
	if got, err := resolve(t.Context(), e, "v9"); err == nil {
		t.Errorf("a version the repository does not have was pinned at %q", got)
	}
	e.Source = filepath.Join(r.dir, "nothing")
	if got, err := resolve(t.Context(), e, "master"); err == nil {
		t.Errorf("a repository that is not there answered %q", got)
	}
}

// A download's pin is the SHA-256 of the file at the version asked, not the one pinned.
func TestADownloadsPinIsTheSumAtTheVersionAsked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/2/f" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("two"))
	}))
	t.Cleanup(srv.Close)
	e := versions.Entry{Name: "f", Kind: versions.Download, Source: srv.URL + "/{version}/f", Version: "1"}
	sum := sha256.Sum256([]byte("two"))
	if got, err := resolve(t.Context(), e, "2"); err != nil || got != hex.EncodeToString(sum[:]) {
		t.Errorf("pin %q, %v", got, err)
	}
}

// docker is a stand-in for the docker CLI that prints the digest in digestFile, or fails when
// there is none.
func docker(t *testing.T) (digestFile string) {
	t.Helper()
	dir := t.TempDir()
	digestFile = filepath.Join(dir, "digest")
	script := "#!/bin/sh\nexec cat " + digestFile + "\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil { //nolint:gosec // an executable is the point
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return digestFile
}

// An image's pin is the digest its tag names now, as docker reads it.
func TestAnImagesPinIsItsDigest(t *testing.T) {
	digestFile := docker(t)
	e := versions.Entry{Name: "go", Kind: versions.Image, Source: "golang"}
	if got, err := resolve(t.Context(), e, "1.27"); err == nil {
		t.Errorf("docker failed and the pin is %q", got)
	}
	if err := os.WriteFile(digestFile, []byte("sha256:abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := resolve(t.Context(), e, "1.27"); err != nil || got != "sha256:abc" {
		t.Errorf("pin %q, %v", got, err)
	}
}

// Each track finds the newest its own way; these are the ones that need no network.
func TestTheNewestIsWhatTheTrackFinds(t *testing.T) {
	r := newGitRepo(t)
	for _, tc := range []struct {
		name  string
		entry versions.Entry
		want  string // "" is an error
	}{
		{"a git entry by its tags", versions.Entry{Kind: versions.Git, Version: "v1.0.0", Track: "tags " + r.dir}, "v1.2.0"},
		{"a tags track whose repository is not there", versions.Entry{Kind: versions.Git, Version: "v1.0.0", Track: "tags " + filepath.Join(r.dir, "nothing")}, ""},
		{"a branch is its own newest", versions.Entry{Version: "master", Track: "branch"}, "master"},
		{"a commit is its branch's head", versions.Entry{Track: "commit " + r.dir + " master"}, r.head},
		// Neither is GitHub, so the error says which repository was asked.
		{"a release of the repository the track names", versions.Entry{Source: "https://example.com/a", Track: "github-release https://example.com/b"}, ""},
		{"a PyPI package whose name is no URL", versions.Entry{Source: "a\x7f", Track: "pypi"}, ""},
		{"a track check does not know", versions.Entry{Track: "rumour"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newest(t.Context(), tc.entry)
			if tc.want == "" && err == nil {
				t.Fatalf("newest %q, want an error", got)
			}
			if tc.want != "" && (err != nil || got != tc.want) {
				t.Errorf("newest %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	_, err := newest(t.Context(), versions.Entry{Source: "https://example.com/a", Track: "github-release https://example.com/b"})
	if !strings.Contains(err.Error(), "https://example.com/b") {
		t.Errorf("the track's repository is not the one asked: %v", err)
	}
}

// Check says where each entry stands: at its newest, behind it, behind at the same version because
// the name points elsewhere now, or a note saying why it cannot tell.
func TestCheckSaysWhereEachEntryStands(t *testing.T) {
	r := newGitRepo(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("v1.2.0's file"))
	}))
	t.Cleanup(srv.Close)
	sum := sha256.Sum256([]byte("v1.2.0's file"))
	missing := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(missing.Close)
	digestFile := docker(t)
	if err := os.WriteFile(digestFile, []byte("sha256:new\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	git := versions.Entry{Name: "g", Kind: versions.Git, Source: r.dir, Version: "master", Track: "branch"}
	moved, gone := git, git
	moved.Name, moved.Pin = "moved", r.second
	gone.Name, gone.Source = "gone", filepath.Join(r.dir, "nothing")
	git.Pin = r.head
	image := versions.Entry{Name: "i", Kind: versions.Image, Source: "golang", Version: "1.27", Pin: "sha256:old", Track: "digest"}
	// Behind by its version, and its file at that version is the same bytes: still behind, since
	// only a file tracked by a commit is excused.
	download := versions.Entry{Name: "d", Kind: versions.Download, Source: srv.URL + "/{version}", Version: "v1.0.0",
		Pin: hex.EncodeToString(sum[:]), Track: "tags " + r.dir}
	current := download
	current.Name, current.Version, current.Pin = "current", "v1.2.0", strings.Repeat("0", 64)
	// A file tracked by a commit is at its branch's head, behind it with the same bytes, or behind
	// it where the file cannot be read; and a git entry tracked by a commit is not a file.
	atHead := versions.Entry{Name: "at-head", Kind: versions.Download, Source: srv.URL + "/{version}", Version: r.head,
		Pin: hex.EncodeToString(sum[:]), Track: "commit " + r.dir + " master"}
	unread := atHead
	unread.Name, unread.Version, unread.Source = "unread", r.first, missing.URL+"/{version}"
	gitByCommit := versions.Entry{Name: "git-by-commit", Kind: versions.Git, Source: r.dir, Version: r.first, Pin: r.head,
		Track: "commit " + r.dir + " master"}
	follows := versions.Entry{Name: "f", Track: "follows spin", Note: "spin says"}
	unknown := versions.Entry{Name: "u", Track: "rumour"}

	got := Check(t.Context(), &versions.Versions{Entries: []versions.Entry{git, moved, gone, image, download, current, atHead, unread, gitByCommit, follows, unknown}})
	want := []struct {
		newest string
		behind bool
		note   string
	}{
		{"master", false, ""},
		{"master", true, "master is now " + r.head},
		{"master", false, "ls-remote"},
		{"1.27", true, "1.27 is now sha256:new"},
		{"v1.2.0", true, ""},
		{"v1.2.0", false, ""},
		{r.head, false, ""},
		{r.head, true, "404"},
		{r.head, true, ""},
		{"", false, "follows spin: spin says"},
		{"", false, "which check does not know"},
	}
	for i, w := range want {
		st := got[i]
		if st.Newest != w.newest || st.Behind != w.behind || !strings.Contains(st.Note, w.note) || (w.note == "" && st.Note != "") {
			t.Errorf("%s: %+v, want newest %q behind %v note %q", st.Entry.Name, st, w.newest, w.behind, w.note)
		}
	}
}
