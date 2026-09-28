package versions

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fixture is a versions.yaml with an entry of every kind.
var fixture = filepath.Join("testdata", File)

// A file of every kind reads, and a bump of any entry to what it is writes it back byte for byte:
// every comment, and every other entry, is as it was.
func TestTheFileReadsAndRewritesItselfUnchanged(t *testing.T) {
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	v, err := parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range v.Entries {
		out, err := v.Set(e.Name, e.Version, e.Pin)
		if err != nil {
			t.Fatalf("%s: %v", e.Name, err)
		}
		if string(out) != string(raw) {
			t.Fatalf("rewriting %s as it is changed the file:\n%s", e.Name, out)
		}
	}
}

// A bump changes its entry and nothing else, and refuses a pin that is not its kind's.
func TestABumpChangesItsEntryAlone(t *testing.T) {
	raw := []byte(`# pins
- name: alloy
  kind: git
  source: https://github.com/grafana/alloy
  version: v1.20.0
  pin: 352ab819cf06611640a7e82048aa0671d24bbfb3
  track: github-release
  # keep me
- name: go
  kind: image
  source: golang
  version: 1.27.1-trixie
  pin: sha256:433790e515d27dc6003e847e644cc0af956985cf315c1c58a3b73ee2dd305183
  track: go
`)
	for _, tc := range []struct {
		name, entry, version, pin string
		// want is the file the bump writes, "" for one it refuses.
		want string
	}{
		{name: "a git pin that is not a commit", entry: "alloy", version: "v1.21.0", pin: "not a commit"},
		{name: "an image pin that is not a digest", entry: "go", version: "1.27.2-trixie", pin: strings.Repeat("a", 40)},
		{name: "an entry the file does not have", entry: "rust", version: "1.90.0", pin: "sha256:" + strings.Repeat("a", 64)},
		{name: "a commit", entry: "alloy", version: "v1.21.0", pin: strings.Repeat("a", 40),
			want: strings.NewReplacer("v1.20.0", "v1.21.0", "352ab819cf06611640a7e82048aa0671d24bbfb3", strings.Repeat("a", 40)).Replace(string(raw))},
		{name: "a digest", entry: "go", version: "1.27.2-trixie", pin: "sha256:" + strings.Repeat("b", 64),
			want: strings.NewReplacer("1.27.1-trixie", "1.27.2-trixie", "433790e515d27dc6003e847e644cc0af956985cf315c1c58a3b73ee2dd305183", strings.Repeat("b", 64)).Replace(string(raw))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			out, err := v.Set(tc.entry, tc.version, tc.pin)
			switch {
			case tc.want == "" && err == nil:
				t.Errorf("the bump was taken, and wrote\n%s", out)
			case tc.want != "" && err != nil:
				t.Errorf("the bump was refused: %v", err)
			case tc.want != "" && string(out) != tc.want:
				t.Errorf("the bump wrote\n%s\nwant\n%s", out, tc.want)
			}
		})
	}
}

// Each kind is handed to a Dockerfile under the names it declares.
func TestEachKindIsItsBuildArguments(t *testing.T) {
	for _, tc := range []struct {
		e    Entry
		want map[string]string
	}{
		{Entry{Name: "go", Kind: Image, Source: "golang", Version: "1.27.1", Pin: "sha256:x"}, map[string]string{"GO_IMAGE": "golang:1.27.1@sha256:x"}},
		{Entry{Name: "grafana-prometheus-datasource", Kind: Git, Version: "v1", Pin: "c"},
			map[string]string{"GRAFANA_PROMETHEUS_DATASOURCE_VERSION": "v1", "GRAFANA_PROMETHEUS_DATASOURCE_COMMIT": "c"}},
		{Entry{Name: "qboot", Kind: Git, Version: "master", Pin: "c", Archive: "a"},
			map[string]string{"QBOOT_VERSION": "master", "QBOOT_COMMIT": "c", "QBOOT_ARCHIVE_SHA256": "a"}},
		{Entry{Name: "kernel", Kind: Download, Version: "7.2.8", Pin: "s"}, map[string]string{"KERNEL_VERSION": "7.2.8", "KERNEL_SHA256": "s"}},
		{Entry{Name: "google-crc32c", Kind: PyPI, Source: "google-crc32c", Version: "1.7.1"}, map[string]string{"GOOGLE_CRC32C_VERSION": "1.7.1"}},
		{Entry{Name: "debian-snapshot", Kind: Date, Version: "20260914T000000Z"}, map[string]string{"DEBIAN_SNAPSHOT": "20260914T000000Z"}},
	} {
		got := tc.e.Args()
		if len(got) != len(tc.want) {
			t.Errorf("%s: %v, want %v", tc.e.Name, got, tc.want)
		}
		for k, v := range tc.want {
			if got[k] != v {
				t.Errorf("%s: %s=%q, want %q", tc.e.Name, k, got[k], v)
			}
		}
	}
}

// A download's URL is its source with the version, and the version's major number, put in.
func TestADownloadIsFetchedFromItsVersionsURL(t *testing.T) {
	for _, tc := range []struct {
		version, source, want string
	}{
		{"7.2.8", "https://cdn.kernel.org/pub/linux/kernel/v{major}.x/linux-{version}.tar.xz", "https://cdn.kernel.org/pub/linux/kernel/v7.x/linux-7.2.8.tar.xz"},
		{"3.3.2", "https://s3.amazonaws.com/ec2-downloads-windows/SSMAgent/{version}/debian_amd64/amazon-ssm-agent.deb",
			"https://s3.amazonaws.com/ec2-downloads-windows/SSMAgent/3.3.2/debian_amd64/amazon-ssm-agent.deb"},
		{"12", "https://example.com/{major}/{version}", "https://example.com/12/12"},
	} {
		e := Entry{Name: "x", Kind: Download, Version: tc.version, Source: tc.source}
		if got := e.URL(); got != tc.want {
			t.Errorf("%s at %s: %s, want %s", tc.source, tc.version, got, tc.want)
		}
	}
}

// A value is replaced in the style it is written in, and whatever else is on its line - quotes, a
// comment, the rest of a flow mapping - is as it was.
func TestABumpKeepsHowTheValueIsWritten(t *testing.T) {
	pin := strings.Repeat("b", 40)
	for _, tc := range []struct {
		name, raw, want string
	}{
		{
			name: "double quotes",
			raw:  "- name: a\n  kind: git\n  source: https://x\n  version: \"v1\"\n  pin: \"" + strings.Repeat("a", 40) + "\"\n  track: github-release\n",
			want: "- name: a\n  kind: git\n  source: https://x\n  version: \"v2\"\n  pin: \"" + pin + "\"\n  track: github-release\n",
		},
		{
			name: "single quotes",
			raw:  "- name: a\n  kind: git\n  source: https://x\n  version: 'v1'\n  pin: " + strings.Repeat("a", 40) + "\n  track: github-release\n",
			want: "- name: a\n  kind: git\n  source: https://x\n  version: 'v2'\n  pin: " + pin + "\n  track: github-release\n",
		},
		{
			name: "a comment after the value",
			raw:  "- name: a\n  kind: git\n  source: https://x\n  version: v1 # the LTS\n  pin: " + strings.Repeat("a", 40) + "\n  track: github-release\n",
			want: "- name: a\n  kind: git\n  source: https://x\n  version: v2 # the LTS\n  pin: " + pin + "\n  track: github-release\n",
		},
		{
			name: "a flow mapping",
			raw:  "- {name: a, kind: git, source: https://x, version: v1, pin: " + strings.Repeat("a", 40) + ", track: github-release}\n",
			want: "- {name: a, kind: git, source: https://x, version: v2, pin: " + pin + ", track: github-release}\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := parse([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			out, err := v.Set("a", "v2", pin)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != tc.want {
				t.Errorf("the bump wrote\n%s\nwant\n%s", out, tc.want)
			}
		})
	}
}

// A bump pins the version it is given, or the newest its track finds, and answers the entry as the
// file it answers has it; the entry v holds is left as it was.
func TestABumpIsToTheVersionGivenOrTheNewest(t *testing.T) {
	raw := []byte("- name: debian-snapshot\n  kind: date\n  version: 20260101T000000Z\n  track: today\n")
	today := time.Now().UTC().Format("20060102") + "T000000Z"
	for _, tc := range []struct{ given, want string }{
		{"20260202T000000Z", "20260202T000000Z"},
		{"", today},
	} {
		v, err := parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		e, out, err := v.Bump(t.Context(), "debian-snapshot", tc.given)
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
	e := Entry{Name: "icons", Source: "https://cdn/{version}/Icon-package_{version}.zip", Track: "page " + srv.URL}
	if got, err := Newest(t.Context(), e); err != nil || got != "07312026.abc" {
		t.Errorf("Newest = %q, %v", got, err)
	}
	e.Source = "https://cdn/Other_{version}.zip"
	if _, err := Newest(t.Context(), e); err == nil {
		t.Error("a page that links to no such file said a version")
	}
}

// An entry whose archive only a build can compute is not bumped, since the new commit with the
// old archive's sum is a build that fails on the checksum.
func TestAnEntryWithAnArchiveIsNotBumped(t *testing.T) {
	raw := []byte("- name: qboot\n  kind: git\n  source: https://x\n  version: master\n  pin: " + strings.Repeat("a", 40) +
		"\n  archive: " + strings.Repeat("b", 64) + "\n  track: branch\n")
	v, err := parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, out, err := v.Bump(t.Context(), "qboot", ""); err == nil {
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
	e := Entry{Name: "qemu", Kind: Download, Source: srv.URL + "/qemu-{version}.tar.xz", Version: "11.0.0"}
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
	e := Entry{Name: "check-config", Kind: Download, Source: srv.URL + "/{version}/check-config.sh",
		Version: first, Track: "commit " + repo + " master"}
	pin, err := Resolve(t.Context(), e, first)
	if err != nil {
		t.Fatal(err)
	}
	e.Pin = pin

	if st := Check(t.Context(), &Versions{Entries: []Entry{e}})[0]; st.Behind {
		t.Errorf("the branch moved and the file did not, and check calls it behind: %+v", st)
	}
	body = "a changed file"
	if st := Check(t.Context(), &Versions{Entries: []Entry{e}})[0]; !st.Behind {
		t.Errorf("the file changed, and check does not call it behind: %+v", st)
	}
}
