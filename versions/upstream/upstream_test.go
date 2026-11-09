package upstream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// A kernel stays in the series it is pinned in, whatever else kernel.org has released, and a series
// nothing lists is over rather than a failure to read kernel.org.
func TestKernelStaysInItsSeries(t *testing.T) {
	releases := []kernelRelease{{"7.4-rc1", "mainline"}, {"7.3.1", "stable"}, {"7.2.15", "stable"}, {"6.18.60", "longterm"}, {"next-20261029", "linux-next"}}
	for version, want := range map[string]string{
		"7.3": "7.3.1", "7.3.1": "7.3.1", "7.2.8": "7.2.15", "6.18": "6.18.60",
	} {
		if got, err := kernelSeries(version, releases); err != nil || got != want {
			t.Errorf("%s: newest %q, %v; want %q", version, got, err, want)
		}
	}
	// Its first tarball, while mainline alone lists it.
	if got, err := kernelSeries("7.4", []kernelRelease{{"7.4", "mainline"}, {"7.3.9", "stable"}}); err != nil || got != "7.4" {
		t.Errorf("a first tarball: newest %q, %v", got, err)
	}
	// 7.4-rc1 is no release of 7.4, and 7.1 is none of 7.15's.
	for _, version := range []string{"7.4", "7.1.4"} {
		if got, err := kernelSeries(version, append(releases, kernelRelease{"7.15.2", "stable"})); !errors.Is(err, errSeriesOver) {
			t.Errorf("%s: newest %q, %v; want the series over", version, got, err)
		}
	}
	if _, err := kernelSeries("7", releases); err == nil || errors.Is(err, errSeriesOver) {
		t.Errorf("a bare major: %v, want refused as no release", err)
	}
}

// A kernel says it could not read kernel.org, not that kernel.org lists nothing.
func TestAKernelSaysKernelOrgDidNotAnswer(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := newest(ctx, versions.Entry{Version: "7.2.8", Track: "kernel"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("%v, want kernel.org's request's error", err)
	}
}

// A pinned file is the URL's file in the directory, kept only as the bytes its entry pins: one
// already there and pinned is answered without a download, one there and not pinned is downloaded
// again, and a download that is not the pin leaves neither the file nor what it was written to.
func TestAPinnedFileIsOnlyThePinnedBytes(t *testing.T) {
	var served atomic.Int32
	body := "the package"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	sumOf := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:])
	}
	for _, tc := range []struct {
		name      string
		there     string // what the directory holds before, if anything
		pin       string
		downloads int32
		wantErr   string
	}{
		{name: "fresh", pin: sumOf(body), downloads: 1},
		{name: "cached", there: body, pin: sumOf(body), downloads: 0},
		{name: "replaced", there: "something else", pin: sumOf(body), downloads: 1},
		{name: "unpinned", pin: sumOf("another package"), downloads: 1, wantErr: "unpinned is not what versions.yaml pins"},
		{name: "unpinned-there", there: "another package", pin: sumOf("a third"), downloads: 1, wantErr: "is not what versions.yaml pins"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cache")
			file := filepath.Join(dir, "pkg-1.2.3.tgz")
			if tc.there != "" {
				require.NoError(t, os.MkdirAll(dir, 0o750))
				require.NoError(t, os.WriteFile(file, []byte(tc.there), 0o600))
			}
			served.Store(0)
			e := versions.Entry{Name: tc.name, Kind: versions.Download, Source: srv.URL + "/pkg-{version}.tgz", Version: "1.2.3", Pin: tc.pin}

			got, err := PinnedFile(t.Context(), e, dir)
			assert.Equal(t, tc.downloads, served.Load(), "downloads")
			left, _ := filepath.Glob(filepath.Join(dir, "*.part"))
			assert.Empty(t, left, "a download's own file outlives it")
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				raw, rerr := os.ReadFile(file) //nolint:gosec // the test's own file
				if tc.there == "" {
					assert.ErrorIs(t, rerr, os.ErrNotExist, "a refused download is the file")
				} else {
					assert.Equal(t, tc.there, string(raw), "a refused download replaced the file")
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, file, got)
			raw, err := os.ReadFile(got) //nolint:gosec // the test's own file
			require.NoError(t, err)
			assert.Equal(t, body, string(raw))
		})
	}

	_, err := PinnedFile(t.Context(), versions.Entry{Name: "go", Kind: versions.Image}, t.TempDir())
	require.ErrorContains(t, err, "go is a image, not a download")
}

// A release's tag is the version the pin spells: a prefix the track names taken off, and a v kept
// only when the pin has one.
func TestAReleasesTagIsTheVersionThePinSpells(t *testing.T) {
	for _, tc := range []struct {
		tag, prefix string
		withV       bool
		want        string
	}{
		{"v3.54.0", "", false, "3.54.0"},
		{"v3.54.0", "", true, "v3.54.0"},
		{"bin-20261109.193146", "bin-", false, "20261109.193146"},
		{"bin-v2", "bin-", true, "v2"},
	} {
		if got, err := releaseVersion(tc.tag, tc.prefix, tc.withV); err != nil || got != tc.want {
			t.Errorf("%s with %q: %q, %v; want %q", tc.tag, tc.prefix, got, err, tc.want)
		}
	}
	if _, err := releaseVersion("v3.54.0", "bin-", false); err == nil {
		t.Error("a release that is not bin-<version> was read as one")
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// serve answers each URL in files with its body and every other with 404, as the default client's
// transport for the test.
func serve(t *testing.T, files map[string]string) {
	t.Helper()
	before := http.DefaultClient.Transport
	http.DefaultClient.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		body, ok := files[r.URL.String()]
		status := http.StatusOK
		if !ok {
			status = http.StatusNotFound
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Request: r, Header: http.Header{}}, nil
	})
	t.Cleanup(func() { http.DefaultClient.Transport = before })
}

func sumOf(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

const perPlatform = `- name: go-tools
  kind: download
  source: https://example.test/bin-{version}/go-tools_{os}_{arch}.tar.gz
  version: "1.0"
  pins:
    linux/amd64: ` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `
    darwin/arm64: ` + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" + `
  track: github-release https://github.com/spin-stack/go-tools bin-
`

// A bump of a download pinned per platform hashes each platform's file of the new version, and is
// refused whole when the release lacks one of them.
func TestABumpPinsEveryPlatformsFile(t *testing.T) {
	serve(t, map[string]string{
		"https://example.test/bin-2.0/go-tools_linux_amd64.tar.gz":  "linux",
		"https://example.test/bin-2.0/go-tools_darwin_arm64.tar.gz": "darwin",
		"https://example.test/bin-3.0/go-tools_linux_amd64.tar.gz":  "linux 3",
	})
	v, err := versions.Parse([]byte(perPlatform))
	if err != nil {
		t.Fatal(err)
	}
	e, out, err := Bump(t.Context(), v, "go-tools", "2.0")
	if err != nil {
		t.Fatal(err)
	}
	if e.Pins["linux/amd64"] != sumOf("linux") || e.Pins["darwin/arm64"] != sumOf("darwin") ||
		!strings.Contains(string(out), "linux/amd64: "+sumOf("linux")+"\n") || !strings.Contains(string(out), `version: "2.0"`) {
		t.Errorf("bumped: %+v\n%s", e, out)
	}
	if _, _, err := Bump(t.Context(), v, "go-tools", "3.0"); err == nil || !strings.Contains(err.Error(), "darwin/arm64") {
		t.Errorf("a release without darwin/arm64's file was pinned: %v", err)
	}
}

// What is fetched of a download pinned per platform is this platform's file, held to its pin.
func TestAFetchIsThisPlatformsFile(t *testing.T) {
	own := "https://example.test/bin-1.0/go-tools_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	serve(t, map[string]string{own: "mine"})
	e := versions.Entry{Name: "go-tools", Kind: versions.Download, Version: "1.0",
		Source: "https://example.test/bin-{version}/go-tools_{os}_{arch}.tar.gz",
		Pins:   map[string]string{runtime.GOOS + "/" + runtime.GOARCH: sumOf("mine")}}
	file, err := PinnedFile(t.Context(), e, t.TempDir())
	if err != nil || filepath.Base(file) != filepath.Base(own) {
		t.Fatalf("fetched %s, %v", file, err)
	}
	if raw, err := Pinned(t.Context(), e, 1<<10); err != nil || string(raw) != "mine" {
		t.Errorf("Pinned = %q, %v", raw, err)
	}
	e.Pins = map[string]string{"plan9/mips": sumOf("mine")}
	if _, err := PinnedFile(t.Context(), e, t.TempDir()); err == nil {
		t.Error("a platform the entry pins nothing for fetched a file")
	}
	if _, err := Pinned(t.Context(), e, 1<<10); err == nil {
		t.Error("a platform the entry pins nothing for read a file")
	}
}

// One platform pinned under pins is still a file per platform: the bump hashes its file, not the
// source with {os} and {arch} in it.
func TestABumpOfOnePlatformsPinsHashesItsFile(t *testing.T) {
	serve(t, map[string]string{"https://example.test/bin-2.0/go-tools_linux_amd64.tar.gz": "linux"})
	v, err := versions.Parse([]byte(strings.Replace(perPlatform, "    darwin/arm64: "+strings.Repeat("b", 64)+"\n", "", 1)))
	if err != nil {
		t.Fatal(err)
	}
	e, _, err := Bump(t.Context(), v, "go-tools", "2.0")
	if err != nil || e.Pins["linux/amd64"] != sumOf("linux") {
		t.Errorf("bumped: %+v, %v", e, err)
	}
}
