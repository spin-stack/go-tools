package upstream

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/internal/gittest"
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
	if got, err := newest(t.Context(), &versions.Versions{}, e); err != nil || got != "07312026.abc" {
		t.Errorf("newest = %q, %v", got, err)
	}
	e.Source = "https://cdn/Other_{version}.zip"
	if _, err := newest(t.Context(), &versions.Versions{}, e); err == nil {
		t.Error("a page that links to no such file said a version")
	}
}

// A Launchpad source is the version its series publishes in the pocket the track names, never the
// -proposed upload the source's page lists first, and without its epoch.
func TestALaunchpadSourceIsWhatItsPocketPublishes(t *testing.T) {
	published := func(source, series, pocket string) string {
		return "https://api.launchpad.net/1.0/ubuntu/+archive/primary?distro_series=https%3A%2F%2Fapi.launchpad.net%2F1.0%2Fubuntu%2F" +
			series + "&exact_match=true&pocket=" + pocket + "&source_name=" + source + "&status=Published&ws.op=getPublishedSources"
	}
	serve(t, map[string]string{
		published("systemd", "tenacious", "Release"):  `{"total_size": 1, "entries": [{"source_package_version": "262.1-1ubuntu1"}]}`,
		published("systemd", "tenacious", "Proposed"): `{"total_size": 1, "entries": [{"source_package_version": "262.2-1ubuntu1"}]}`,
		published("openssh", "resolute", "Updates"):   `{"total_size": 1, "entries": [{"source_package_version": "1:10.2p1-2ubuntu3.8"}]}`,
		published("openssh", "resolute", "Proposed"):  `{"total_size": 0, "entries": []}`,
	})
	for track, want := range map[string]string{
		"launchpad systemd tenacious Release": "262.1-1ubuntu1",
		"launchpad openssh resolute Updates":  "10.2p1-2ubuntu3.8",
	} {
		if got, err := newest(t.Context(), &versions.Versions{}, versions.Entry{Track: track}); err != nil || got != want {
			t.Errorf("%s: newest %q, %v; want %q", track, got, err, want)
		}
	}
	for _, track := range []string{
		"launchpad openssh resolute Proposed",
		"launchpad systemd tenacious Security",
		"launchpad systemd tenacious",
		"launchpad systemd tenacious Release extra",
	} {
		if got, err := newest(t.Context(), &versions.Versions{}, versions.Entry{Track: track}); err == nil {
			t.Errorf("%s: newest %q, want refused", track, got)
		}
	}
}

// A launchpad track that names several pockets is the newest version any of them publishes, by
// Debian ordering: a security fix the moment Security has it, an update the moment Updates has
// it, and never the -proposed upload no stock apt can fetch.
func TestALaunchpadSourceIsTheNewestOfItsPockets(t *testing.T) {
	published := func(source, series, pocket string) string {
		return "https://api.launchpad.net/1.0/ubuntu/+archive/primary?distro_series=https%3A%2F%2Fapi.launchpad.net%2F1.0%2Fubuntu%2F" +
			series + "&exact_match=true&pocket=" + pocket + "&source_name=" + source + "&status=Published&ws.op=getPublishedSources"
	}
	one := func(v string) string {
		return `{"total_size": 1, "entries": [{"source_package_version": "` + v + `"}]}`
	}
	none := `{"total_size": 0, "entries": []}`
	serve(t, map[string]string{
		published("openssh", "resolute", "Release"):   one("1:10.2p1-2ubuntu3"),
		published("openssh", "resolute", "Updates"):   one("1:10.2p1-2ubuntu3.8"),
		published("openssh", "resolute", "Security"):  one("1:10.2p1-2ubuntu3.10"),
		published("openssh", "resolute", "Proposed"):  one("1:10.2p1-2ubuntu3.11"),
		published("openssh", "questing", "Release"):   one("1:10.0p1-5ubuntu5"),
		published("openssh", "questing", "Updates"):   one("1:10.0p1-5ubuntu5.2"),
		published("openssh", "questing", "Security"):  one("1:10.0p1-5ubuntu5.1"),
		published("openssh", "questing", "Proposed"):  one("1:10.0p1-5ubuntu5.3"),
		published("systemd", "tenacious", "Release"):  one("262.1-1ubuntu1"),
		published("systemd", "tenacious", "Updates"):  none,
		published("systemd", "tenacious", "Security"): none,
		published("systemd", "tenacious", "Proposed"): one("262.2-1ubuntu1"),
		published("systemd", "noble", "Release"):      none,
		published("systemd", "noble", "Updates"):      none,
	})
	for track, want := range map[string]string{
		"launchpad openssh resolute Release,Updates,Security":  "10.2p1-2ubuntu3.10",
		"launchpad openssh questing Release,Updates,Security":  "10.0p1-5ubuntu5.2",
		"launchpad systemd tenacious Release,Updates,Security": "262.1-1ubuntu1",
	} {
		if got, err := newest(t.Context(), &versions.Versions{}, versions.Entry{Track: track}); err != nil || got != want {
			t.Errorf("%s: newest %q, %v; want %q", track, got, err, want)
		}
	}
	for _, track := range []string{
		"launchpad systemd noble Release,Updates",
		"launchpad systemd noble Release,Security",
	} {
		if got, err := newest(t.Context(), &versions.Versions{}, versions.Entry{Track: track}); err == nil {
			t.Errorf("%s: newest %q, want refused", track, got)
		}
	}
}

// Debian versions order as dpkg orders them, not as strings.
func TestDebianVersionsOrderAsDpkgOrdersThem(t *testing.T) {
	for _, older := range [][2]string{
		{"3.8", "3.10"},
		{"10.2p1-2ubuntu3.8", "10.2p1-2ubuntu3.10"},
		{"1.0-2", "1.0-10"},
		{"2.0", "1:1.0"},
		{"1.0~rc1", "1.0"},
		{"1.0", "1.0a"},
		{"1.0a", "1.0+b"},
		{"262.1-1ubuntu1", "262.1-1ubuntu1.1"},
		{"1.2-3-1", "1.2-4-1"},
		{"1.0", "1.0-1"},
		{"1.0A", "1.0+"},
		{"1.0Z", "1.0a"},
	} {
		if c := debianCompare(older[0], older[1]); c >= 0 {
			t.Errorf("debianCompare(%q, %q) = %d, want below zero", older[0], older[1], c)
		}
		if c := debianCompare(older[1], older[0]); c <= 0 {
			t.Errorf("debianCompare(%q, %q) = %d, want above zero", older[1], older[0], c)
		}
	}
	if c := debianCompare("1.01-1", "1.1-1"); c != 0 {
		t.Errorf("debianCompare(1.01-1, 1.1-1) = %d, want 0", c)
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
		if r.URL.Path != "/qemu-11.1.1.tar.xz" && r.URL.Path != "/qemu-11.0.0.tar.xz" && r.URL.Path != "/qemu-v11.1.1.tar.xz" {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	e := versions.Entry{Name: "qemu", Kind: versions.Download, Source: srv.URL + "/qemu-{version}.tar.xz", Version: "11.0.0"}
	for _, tc := range []struct {
		pin  string
		tags []string
		want string
	}{
		{"11.0.0", []string{"11.1.2", "11.1.1", "11.0.0"}, "11.1.1"},
		{"11.0.0", []string{"11.1.2", "11.0.0"}, "11.0.0"},
		{"11.0.0", []string{"11.0.0", "10.2.0"}, "11.0.0"},
		// A release candidate is before the release of its numbers and after every release before
		// it: spin-machine pinned 11.2.0-rc0, and check could not read the row.
		{"11.1.1-rc0", []string{"11.1.2", "11.1.1", "11.0.0"}, "11.1.1"},
		{"11.1.2-rc0", []string{"11.1.2", "11.1.1", "11.0.0"}, "11.1.2-rc0"},
		{"11.2.0-rc0", []string{"11.1.2", "11.1.1", "11.0.0"}, "11.2.0-rc0"},
		{"v11.0.0-rc3", []string{"v11.1.2", "v11.1.1", "v11.0.0"}, "v11.1.1"},
	} {
		e.Version = tc.pin
		got, err := newestPublished(t.Context(), e, tc.tags)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("%s, tags %v: newest %q, want %q", tc.pin, tc.tags, got, tc.want)
		}
	}
	// What starts with no number, or goes on other than with a -suffix, is not ordered: said, and
	// never called current. The served file is not asked.
	for _, pin := range []string{"master", "0123abc", "11.1.1rc0", "11.1.x", "20261122T000000Z", "99999999999999999999.1"} {
		e.Version = pin
		if got, err := newestPublished(t.Context(), e, []string{"11.1.1"}); err == nil || !strings.Contains(err.Error(), "cannot order it") {
			t.Errorf("%s: newest %q, %v; want refused as a version the track cannot order", pin, got, err)
		}
	}
}

// A file pinned to a commit of a branch that moves several times a day is behind when the file
// changes, not when the branch moves: the repository is a local one and the file is served here.
func TestACommitTrackedFileIsBehindOnlyWhenItChanged(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		return gittest.Run(t, repo, args...)
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
		"7.3": "7.3.1", "7.3.1": "7.3.1", "7.2.8": "7.2.15", "6.18": "6.18.60", "7.4-rc1": "7.4-rc1",
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
	_, err := newest(ctx, &versions.Versions{}, versions.Entry{Version: "7.2.8", Track: "kernel"})
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

// A release's tag is the version the pin spells: a prefix the track names taken off, a v kept only
// when the pin has one, and an image's variant - what its tag says after the version - kept too.
func TestAReleasesTagIsTheVersionThePinSpells(t *testing.T) {
	for _, tc := range []struct {
		tag, prefix string
		pinned      versions.Entry
		want        string
	}{
		{"v3.54.0", "", versions.Entry{Version: "3.53.0"}, "3.54.0"},
		{"v3.54.0", "", versions.Entry{Version: "v3.53.0"}, "v3.54.0"},
		{"bin-20261109.193146", "bin-", versions.Entry{Version: "20261108.1"}, "20261109.193146"},
		{"bin-v2", "bin-", versions.Entry{Version: "v1"}, "v2"},
		{"v2.16.0", "", versions.Entry{Kind: versions.Image, Version: "v2.15.0-alpine"}, "v2.16.0-alpine"},
		{"v2.16.0", "", versions.Entry{Kind: versions.Image, Version: "v2.15.0"}, "v2.16.0"},
		{"1.0.5", "", versions.Entry{Kind: versions.Image, Version: "1.0.4-slim"}, "1.0.5-slim"},
		// Only an image's tag names a variant: a download's -linux is no part of its version.
		{"v2.16.0", "", versions.Entry{Kind: versions.Download, Version: "v2.15.0-linux"}, "v2.16.0"},
		// A tag that does not start with a version has no variant to keep.
		{"v2.16.0", "", versions.Entry{Kind: versions.Image, Version: "latest"}, "2.16.0"},
	} {
		if got, err := releaseVersion(tc.tag, tc.prefix, tc.pinned); err != nil || got != tc.want {
			t.Errorf("%s with %q for %s: %q, %v; want %q", tc.tag, tc.prefix, tc.pinned.Version, got, err, tc.want)
		}
	}
	if _, err := releaseVersion("v3.54.0", "bin-", versions.Entry{Version: "3.53.0"}); err == nil {
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

// withLead is a release and two files of it: one by its version, spelled without the v, and one
// named by the release's pin.
const withLead = `- name: lead
  kind: download
  source: https://example.test/lead-{version}.tgz
  version: v1.0
  pin: ` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `
  track: tags https://example.test/lead
- name: utils
  kind: download
  source: https://example.test/utils-{version}.tgz
  version: "1.0"
  pin: ` + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" + `
  track: with lead
- name: by-pin
  kind: download
  source: https://example.test/by-pin-{version}.tgz
  version: ` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `
  pin: ` + "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc" + `
  track: with lead pin
`

// A bump of an entry moves every entry with it in the same file, each resolved at what the entry
// is then at; one of them that cannot be resolved refuses the bump whole.
func TestABumpMovesTheEntriesWithIt(t *testing.T) {
	leadSum := sumOf("lead 2")
	serve(t, map[string]string{
		"https://example.test/lead-v2.0.tgz":              "lead 2",
		"https://example.test/utils-2.0.tgz":              "utils 2",
		"https://example.test/by-pin-" + leadSum + ".tgz": "by-pin 2",
		"https://example.test/lead-v3.0.tgz":              "lead 3",
	})
	v, err := versions.Parse([]byte(withLead))
	require.NoError(t, err)
	e, out, err := Bump(t.Context(), v, "lead", "v2.0")
	require.NoError(t, err)
	assert.Equal(t, "v2.0", e.Version)
	assert.Equal(t, leadSum, e.Pin)
	bumped, err := versions.Parse(out)
	require.NoError(t, err)
	for name, want := range map[string][2]string{
		"lead":   {"v2.0", leadSum},
		"utils":  {"2.0", sumOf("utils 2")},
		"by-pin": {leadSum, sumOf("by-pin 2")},
	} {
		got, err := bumped.Get(name)
		require.NoError(t, err)
		assert.Equal(t, want, [2]string{got.Version, got.Pin}, name)
	}
	// v3.0's utils is not published: neither moves.
	_, _, err = Bump(t.Context(), v, "lead", "v3.0")
	assert.ErrorContains(t, err, "utils, with lead")
}

// An entry with another is bumped to what that one is at, and to nothing else: another version is
// the other's bump to make.
func TestAnEntryWithAnotherIsBumpedToItsVersion(t *testing.T) {
	serve(t, map[string]string{"https://example.test/utils-1.0.tgz": "utils 1"})
	v, err := versions.Parse([]byte(withLead))
	require.NoError(t, err)
	e, _, err := Bump(t.Context(), v, "utils", "")
	require.NoError(t, err)
	assert.Equal(t, [2]string{"1.0", sumOf("utils 1")}, [2]string{e.Version, e.Pin})
	e, _, err = Bump(t.Context(), v, "utils", "1.0")
	require.NoError(t, err, "named at the version it is with")
	assert.Equal(t, sumOf("utils 1"), e.Pin)
	_, _, err = Bump(t.Context(), v, "utils", "2.0")
	assert.ErrorContains(t, err, `utils is with lead, at "1.0": bump lead`)
}

// An entry with another whose archive only a build can write is not moved by the other's bump:
// the bump is refused before anything is resolved.
func TestAnEntryWithAnArchiveStopsItsLeadersBump(t *testing.T) {
	file := withLead + `- name: src
  kind: git
  source: https://example.test/src
  version: v1.0
  pin: ` + strings.Repeat("d", 40) + `
  archive: ` + strings.Repeat("e", 64) + `
  track: with lead
`
	v, err := versions.Parse([]byte(file))
	require.NoError(t, err)
	_, _, err = Bump(t.Context(), v, "lead", "v2.0")
	assert.ErrorContains(t, err, "src's archive is what a build's git writes")
}

// contents is what GitHub's contents API answers for a file: its bytes in base64, wrapped as the
// API wraps them.
func contents(file string) string {
	b64 := base64.StdEncoding.EncodeToString([]byte(file))
	return `{"encoding":"base64","content":"` + b64[:10] + `\n` + b64[10:] + `"}`
}

const spinFile = `- name: grafana
  kind: download
  source: https://example.test/grafana-{version}.tgz
  version: 13.4.0
  pin: ` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `
  track: github-release https://github.com/grafana/grafana
- name: day
  kind: date
  version: 20260101T000000Z
  track: today
`

const withSpin = `- name: grafana
  kind: download
  source: https://example.test/g-{version}
  version: v13.3.0
  pin: ` + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" + `
  track: with spin-stack/spin grafana
`

// An entry with another repository's is at what that repository's versions.yaml says now, read
// through GitHub's API: check says it is behind, a bump takes it, and another version is refused.
func TestAnEntryWithAnotherRepositorysMovesWithIt(t *testing.T) {
	serve(t, map[string]string{
		"https://api.github.com/repos/spin-stack/spin/contents/versions.yaml": contents(spinFile),
		"https://example.test/g-v13.4.0":                                      "grafana 13.4",
	})
	v, err := versions.Parse([]byte(withSpin))
	require.NoError(t, err)
	st := Check(t.Context(), v)
	require.Len(t, st, 1)
	assert.Equal(t, "v13.4.0", st[0].Newest)
	assert.True(t, st[0].Behind, st[0].Note)

	e, _, err := Bump(t.Context(), v, "grafana", "")
	require.NoError(t, err)
	assert.Equal(t, [2]string{"v13.4.0", sumOf("grafana 13.4")}, [2]string{e.Version, e.Pin})
	_, _, err = Bump(t.Context(), v, "grafana", "v13.5.0")
	assert.ErrorContains(t, err, `grafana is with spin-stack/spin's grafana, at "v13.4.0": it moves when that one does`)
}

// An entry with another repository's release is at what the versions.yaml of its latest release's
// tag says, not its main's: a build of that release is what the entry is built with.
func TestAnEntryWithAnotherRepositorysReleaseMovesWithItsRelease(t *testing.T) {
	released := strings.Replace(spinFile, "version: 13.4.0", "version: 13.3.1", 1)
	serve(t, map[string]string{
		"https://api.github.com/repos/spin-stack/spin/releases/latest":                       `{"tag_name": "v0.9.0+1"}`,
		"https://api.github.com/repos/spin-stack/spin/contents/versions.yaml?ref=v0.9.0%2B1": contents(released),
		"https://api.github.com/repos/spin-stack/spin/contents/versions.yaml":                contents(spinFile),
		"https://example.test/g-v13.3.1":                                                     "grafana 13.3.1",
	})
	v, err := versions.Parse([]byte(strings.Replace(withSpin, "with spin-stack/spin grafana", "with spin-stack/spin@release grafana", 1)))
	require.NoError(t, err)
	e, _, err := Bump(t.Context(), v, "grafana", "")
	require.NoError(t, err)
	assert.Equal(t, [2]string{"v13.3.1", sumOf("grafana 13.3.1")}, [2]string{e.Version, e.Pin})
}

// A repository whose latest release cannot be read, or has no tag, gives no version.
func TestAnotherRepositorysReleaseIsReadOrRefused(t *testing.T) {
	for _, tc := range []struct{ name, answer, want string }{
		{"no release, or private with no token", "", "spin-stack/spin's latest release: versions:"},
		{"a release with no tag", `{"tag_name": ""}`, "spin-stack/spin's latest release has no tag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{"https://api.github.com/repos/spin-stack/spin/contents/versions.yaml": contents(spinFile)}
			if tc.answer != "" {
				files["https://api.github.com/repos/spin-stack/spin/releases/latest"] = tc.answer
			}
			serve(t, files)
			v, err := versions.Parse([]byte(strings.Replace(withSpin, "with spin-stack/spin grafana", "with spin-stack/spin@release grafana", 1)))
			require.NoError(t, err)
			_, err = newest(t.Context(), v, v.Entries[0])
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

// Another repository's file that cannot be read, or read as one, or that lacks what the entry
// takes of it, is an error that says which: never a version.
func TestAnotherRepositorysFileIsReadOrRefused(t *testing.T) {
	api := "https://api.github.com/repos/spin-stack/spin/contents/versions.yaml"
	for _, tc := range []struct{ name, answer, track, want string }{
		{"not there, or private with no token", "", "with spin-stack/spin grafana", "GITHUB_TOKEN or GH_TOKEN"},
		{"not base64", `{"encoding":"none","content":""}`, "with spin-stack/spin grafana", `came as "none", not base64`},
		{"not base64 at all", `{"encoding":"base64","content":"%%%"}`, "with spin-stack/spin grafana", "spin-stack/spin's versions.yaml: illegal base64"},
		{"no versions.yaml", contents("- name: x\n"), "with spin-stack/spin grafana", "spin-stack/spin's versions:"},
		{"no such entry", contents(spinFile), "with spin-stack/spin qemu", "there is no qemu"},
		{"the pin of one that has none", contents(spinFile), "with spin-stack/spin day pin", "grafana is with day's pin, and day has none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{}
			if tc.answer != "" {
				files[api] = tc.answer
			}
			serve(t, files)
			v, err := versions.Parse([]byte(strings.Replace(withSpin, "with spin-stack/spin grafana", tc.track, 1)))
			require.NoError(t, err)
			_, err = newest(t.Context(), v, v.Entries[0])
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

// Two versions are ordered by the numbers they start with, and only when both start with some.
func TestVersionsAreOrderedByTheirNumbers(t *testing.T) {
	for _, tc := range []struct {
		a, b    string
		c       int
		ordered bool
	}{
		{"v20261024.01", "v20261114.01", -1, true},
		{"v1.2.0", "v1.10.0", -1, true},
		{"7.3", "7.3.1", -1, true},
		{"1.27.5-bookworm", "1.27.4-bookworm", 1, true},
		{"11.2.0-rc0", "11.2.0", 0, true},
		{"v3.54.0", "3.54.0", 0, true},
		{"main", "v20261114.01", 0, false},
		{"v20261114.01", "main", 0, false},
		{"51f7e0dea53e6f85aa855753766b688634487168", "358445c95280923876ef3e95658fe45e367462a1", 0, false},
		{"20260101T000000Z", "20270101T000000Z", 0, false},
	} {
		if c, ordered := compare(tc.a, tc.b); ordered != tc.ordered || (ordered && c != tc.c) {
			t.Errorf("%s against %s: %d, ordered %v; want %d, %v", tc.a, tc.b, c, ordered, tc.c, tc.ordered)
		}
	}
}

// A bump to a version whose numbers are lower than the pin's is refused before anything is
// fetched, named or found: website's spin went from v20261114.01 to v20261024.01 with exit 0.
// BumpBackwards is the same bump meant, and moves the entries with it too.
func TestABumpDoesNotMoveAVersionBackwards(t *testing.T) {
	v, err := versions.Parse([]byte(withLead))
	require.NoError(t, err)
	serve(t, nil)
	_, out, err := Bump(t.Context(), v, "lead", "v0.9")
	require.ErrorIs(t, err, ErrBackwards)
	require.ErrorContains(t, err, "lead is at v1.0, and v0.9 is older")
	assert.Nil(t, out)

	leadSum := sumOf("lead 0.9")
	serve(t, map[string]string{
		"https://example.test/lead-v0.9.tgz":              "lead 0.9",
		"https://example.test/utils-0.9.tgz":              "utils 0.9",
		"https://example.test/by-pin-" + leadSum + ".tgz": "by-pin 0.9",
	})
	e, out, err := BumpBackwards(t.Context(), v, "lead", "v0.9")
	require.NoError(t, err)
	assert.Equal(t, "v0.9", e.Version)
	back, err := versions.Parse(out)
	require.NoError(t, err)
	utils, err := back.Get("utils")
	require.NoError(t, err)
	assert.Equal(t, "0.9", utils.Version)

	// The version it is at is no move, and not backwards.
	serve(t, map[string]string{"https://example.test/lead-v1.0.tgz": "lead 1", "https://example.test/utils-1.0.tgz": "utils 1",
		"https://example.test/by-pin-" + sumOf("lead 1") + ".tgz": "by-pin 1"})
	e, _, err = Bump(t.Context(), v, "lead", "v1.0")
	require.NoError(t, err)
	assert.Equal(t, "v1.0", e.Version)

	// The newest an upstream names can be older than the pin: its latest release is not its newest.
	ahead, err := versions.Parse([]byte("- name: tool\n  kind: download\n  source: https://example.test/tool-{version}.tgz\n  version: v2.0.0\n  pin: " +
		strings.Repeat("a", 64) + "\n  track: github-release https://github.com/o/tool\n"))
	require.NoError(t, err)
	serve(t, map[string]string{"https://api.github.com/repos/o/tool/releases/latest": `{"tag_name": "v1.9.0"}`})
	_, _, err = Bump(t.Context(), ahead, "tool", "")
	require.ErrorIs(t, err, ErrBackwards)
	require.ErrorContains(t, err, "tool is at v2.0.0, and v1.9.0 is older")
}

// An entry tracked with another goes where that one is, which a bump of it is for: a file in
// which it is ahead of the entry it is with is one the gate refuses, and the bump mends it.
func TestAnEntryWithAnotherIsMovedBackToIt(t *testing.T) {
	v, err := versions.Parse([]byte(strings.Replace(withLead, `version: "1.0"`, `version: "3.0"`, 1)))
	require.NoError(t, err)
	serve(t, map[string]string{"https://example.test/utils-1.0.tgz": "utils 1"})
	e, _, err := Bump(t.Context(), v, "utils", "")
	require.NoError(t, err)
	assert.Equal(t, "1.0", e.Version)
}

// github makes https://github.com/o/r the repository at dir for every git this process starts,
// as the code under test names it, and answers GitHub's compare of two of its commits with status.
func github(t *testing.T, dir string, compared map[string]string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+dir+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/o/r")
	files := map[string]string{}
	for commits, status := range compared {
		files["https://api.github.com/repos/o/r/compare/"+commits] = `{"status": "` + status + `"}`
	}
	serve(t, files)
}

// A git entry whose version says neither way - a branch moved to another, a branch's commit to a
// tag - is not moved to a commit the pinned one already holds: website pins a commit of main
// until a release holds it, and the release before it would take the site backwards. GitHub is
// asked, since a bump has neither commit; what it cannot be asked is refused, not guessed.
func TestABumpDoesNotMoveACommitBackwards(t *testing.T) {
	r := newGitRepo(t)
	bytes := "- name: g\n  kind: git\n  source: https://github.com/o/r\n  version: master\n  pin: " + r.head + "\n  track: branch\n"
	onMaster, err := versions.Parse([]byte(bytes))
	require.NoError(t, err)

	for status, backwards := range map[string]bool{"behind": true, "ahead": false, "diverged": false, "identical": false} {
		github(t, r.dir, map[string]string{r.head + "..." + r.second: status})
		e, _, err := Bump(t.Context(), onMaster, "g", "topic")
		if backwards {
			require.ErrorIs(t, err, ErrBackwards, status)
			require.ErrorContains(t, err, "g is at "+r.head+", which already holds topic "+r.second)
			continue
		}
		require.NoError(t, err, status)
		assert.Equal(t, r.second, e.Pin, status)
	}

	// GitHub does not answer: refused, as neither backwards nor moved. Told to, it is not asked.
	github(t, r.dir, nil)
	_, _, err = Bump(t.Context(), onMaster, "g", "topic")
	require.ErrorContains(t, err, "could not be asked")
	require.NotErrorIs(t, err, ErrBackwards)
	e, _, err := BumpBackwards(t.Context(), onMaster, "g", "topic")
	require.NoError(t, err)
	assert.Equal(t, r.second, e.Pin)

	// The same commit is no move, and higher numbers are a later release wherever its commit is:
	// neither is asked of GitHub, which answers nothing here.
	e, _, err = Bump(t.Context(), onMaster, "g", "master")
	require.NoError(t, err)
	assert.Equal(t, r.head, e.Pin)
	tagged, err := versions.Parse([]byte(strings.NewReplacer("version: master", "version: v1.0.0", r.head, r.first, "o/r\n", "o/r.git\n").Replace(bytes)))
	require.NoError(t, err)
	t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/o/r.git")
	e, _, err = Bump(t.Context(), tagged, "g", "v1.2.0")
	require.NoError(t, err)
	assert.Equal(t, r.second, e.Pin)
	// Numbers that are the same, or that only one of the two versions has, say neither way, and
	// the commits are asked: a tag moved to an older commit, and a branch's head moved to a tag.
	github(t, r.dir, map[string]string{r.second + "..." + r.first: "behind", r.head + "..." + r.first: "behind"})
	moved, err := versions.Parse([]byte(strings.NewReplacer("version: master", "version: v1.0.0", r.head, r.second).Replace(bytes)))
	require.NoError(t, err)
	_, _, err = Bump(t.Context(), moved, "g", "v1.0.0")
	require.ErrorIs(t, err, ErrBackwards)
	_, _, err = Bump(t.Context(), onMaster, "g", "v1.0.0")
	require.ErrorIs(t, err, ErrBackwards)
	t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/o/r.git")
	// A repository named with .git is the same one to the API.
	serve(t, map[string]string{"https://api.github.com/repos/o/r/compare/" + r.first + "..." + r.head: `{"status": "ahead"}`})
	e, _, err = Bump(t.Context(), tagged, "g", "master")
	require.NoError(t, err)
	assert.Equal(t, r.head, e.Pin)

	// A repository that is not GitHub's has nobody to ask, and moves.
	local, err := versions.Parse([]byte(strings.Replace(bytes, "https://github.com/o/r", "https://example.test/o/r", 1)))
	require.NoError(t, err)
	t.Setenv("GIT_CONFIG_VALUE_0", "https://example.test/o/r")
	serve(t, nil)
	e, _, err = Bump(t.Context(), local, "g", "topic")
	require.NoError(t, err)
	assert.Equal(t, r.second, e.Pin)
}

// A pin on a release candidate follows its series: the next candidate, then the release and its
// stable updates. A candidate of a series kernel.org no longer lists is over.
func TestAKernelCandidateFollowsItsSeries(t *testing.T) {
	for _, tc := range []struct {
		releases []kernelRelease
		want     string
	}{
		{[]kernelRelease{{"7.4-rc5", "mainline"}, {"7.3.9", "stable"}}, "7.4-rc5"},
		{[]kernelRelease{{"7.5-rc1", "mainline"}, {"7.4", "stable"}, {"7.3.9", "stable"}}, "7.4"},
		{[]kernelRelease{{"7.5-rc2", "mainline"}, {"7.4.2", "stable"}}, "7.4.2"},
	} {
		if got, err := kernelSeries("7.4-rc4", tc.releases); err != nil || got != tc.want {
			t.Errorf("7.4-rc4 against %v: %q, %v; want %q", tc.releases, got, err, tc.want)
		}
	}
	if got, err := kernelSeries("7.3.9", []kernelRelease{{"7.4-rc5", "mainline"}, {"7.3.9", "stable"}}); err != nil || got != "7.3.9" {
		t.Errorf("a stable pin: %q, %v", got, err)
	}
	if _, err := kernelSeries("7.4-rc4", []kernelRelease{{"7.6-rc1", "mainline"}, {"7.5.3", "stable"}}); !errors.Is(err, errSeriesOver) {
		t.Errorf("a candidate of a series no longer listed: %v", err)
	}
}
