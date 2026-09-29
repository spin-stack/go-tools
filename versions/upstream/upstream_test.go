package upstream

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spin-stack/go-tools/versions"
)

// A bump pins the version it is given, or the newest its track finds, and answers the entry as the
// file it answers has it; the entry v holds is left as it was.
func TestABumpIsToTheVersionGivenOrTheNewest(t *testing.T) {
	raw := []byte("- name: debian-snapshot\n  kind: date\n  version: 20260101T000000Z\n  track: today\n")
	today := time.Now().UTC().Format("20060102") + "T000000Z"
	for _, tc := range []struct{ given, want string }{
		{"20260202T000000Z", "20260202T000000Z"},
		{"", today},
	} {
		v, err := versions.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		e, out, err := Bump(t.Context(), v, "debian-snapshot", tc.given)
		if err != nil {
			t.Fatal(err)
		}
		if e.Version != tc.want || !strings.Contains(string(out), "version: "+tc.want+"\n") {
			t.Errorf("bumped to %q: %+v\n%s", tc.given, e, out)
		}
		if was, _ := v.Get("debian-snapshot"); was.Version != "20260101T000000Z" {
			t.Errorf("v changed: %+v", was)
		}
	}
}

// A publisher that says what is newest only on a page is read there: the first link whose file is
// the source's, {version} standing for the version.
func TestTheNewestIsWhatThePageLinksTo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<a href="https://cdn/x/Icon-package_07312026.abc.zip">icons</a>
			<a href="https://cdn/x/Toolkit_07312026.def.zip">slides</a>`))
	}))
	t.Cleanup(srv.Close)
	e := versions.Entry{Name: "icons", Source: "https://cdn/{version}/Icon-package_{version}.zip", Track: "page " + srv.URL}
	if got, err := newest(t.Context(), e); err != nil || got != "07312026.abc" {
		t.Errorf("newest = %q, %v", got, err)
	}
	e.Source = "https://cdn/Other_{version}.zip"
	if _, err := newest(t.Context(), e); err == nil {
		t.Error("a page that links to no such file said a version")
	}
}

// An entry whose archive only a build can compute is not bumped, since the new commit with the
// old archive's sum is a build that fails on the checksum.
func TestAnEntryWithAnArchiveIsNotBumped(t *testing.T) {
	raw := []byte("- name: qboot\n  kind: git\n  source: https://x\n  version: master\n  pin: " + strings.Repeat("a", 40) +
		"\n  archive: " + strings.Repeat("b", 64) + "\n  track: branch\n")
	v, err := versions.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, out, err := Bump(t.Context(), v, "qboot", ""); err == nil {
		t.Errorf("the bump was taken, and wrote\n%s", out)
	}
}

// Only a release tag is a version, and 11.10 comes after 11.9.
func TestTheNewestTagIsTheHighestRelease(t *testing.T) {
	for tag, want := range map[string]bool{
		"v11.1.1": true, "1.47.4": true, "v27": true,
		"v11.2.0-rc0": false, "v1.47.4-WIP": false, "stable-2.12": false,
	} {
		if got := release.MatchString(tag); got != want {
			t.Errorf("%s: a release %v, want %v", tag, got, want)
		}
	}
	a, _ := numbers("11.10")
	b, _ := numbers("11.9.9")
	if slices.Compare(a, b) <= 0 {
		t.Error("11.10 is not after 11.9.9")
	}
}

// A tag without its download is not a version to move to: QEMU tagged v11.1.2 before its tarball
// was published, and a bump to it failed on a 404.
func TestTheNewestDownloadIsTheNewestPublished(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/qemu-11.1.1.tar.xz" && r.URL.Path != "/qemu-11.0.0.tar.xz" {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	e := versions.Entry{Name: "qemu", Kind: versions.Download, Source: srv.URL + "/qemu-{version}.tar.xz", Version: "11.0.0"}
	for _, tc := range []struct {
		tags []string
		want string
	}{
		{[]string{"11.1.2", "11.1.1", "11.0.0"}, "11.1.1"},
		{[]string{"11.1.2", "11.0.0"}, "11.0.0"},
		{[]string{"11.0.0", "10.2.0"}, "11.0.0"},
	} {
		got, err := newestPublished(t.Context(), e, tc.tags)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("tags %v: newest %q, want %q", tc.tags, got, tc.want)
		}
	}
}

// A file pinned to a commit of a branch that moves several times a day is behind when the file
// changes, not when the branch moves: the repository is a local one and the file is served here.
func TestACommitTrackedFileIsBehindOnlyWhenItChanged(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "master")
	git("commit", "-q", "--allow-empty", "-m", "one")
	first := git("rev-parse", "HEAD")
	git("commit", "-q", "--allow-empty", "-m", "two")

	body := "the same file"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	e := versions.Entry{Name: "check-config", Kind: versions.Download, Source: srv.URL + "/{version}/check-config.sh",
		Version: first, Track: "commit " + repo + " master"}
	pin, err := resolve(t.Context(), e, first)
	if err != nil {
		t.Fatal(err)
	}
	e.Pin = pin

	if st := Check(t.Context(), &versions.Versions{Entries: []versions.Entry{e}})[0]; st.Behind {
		t.Errorf("the branch moved and the file did not, and check calls it behind: %+v", st)
	}
	body = "a changed file"
	if st := Check(t.Context(), &versions.Versions{Entries: []versions.Entry{e}})[0]; !st.Behind {
		t.Errorf("the file changed, and check does not call it behind: %+v", st)
	}
}

// A pinned download is answered only when it is the bytes its entry pins, and the refusal names
// the entry.
func TestAPinnedDownloadIsOnlyThePinnedBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("the package"))
	}))
	t.Cleanup(srv.Close)
	sumOf := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:])
	}
	for _, tc := range []struct {
		name, pin, wantErr string
	}{
		{name: "pinned", pin: sumOf("the package")},
		{name: "another", pin: sumOf("another package"), wantErr: "another is not what versions.yaml pins"},
	} {
		e := versions.Entry{Name: tc.name, Kind: versions.Download, Source: srv.URL, Pin: tc.pin}
		got, err := Pinned(t.Context(), e, 64)
		switch {
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.wantErr)
		case tc.wantErr == "" && (err != nil || string(got) != "the package"):
			t.Errorf("%s: %q, %v", tc.name, got, err)
		}
	}
}
