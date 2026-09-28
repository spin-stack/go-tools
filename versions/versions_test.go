package versions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	v, err := Parse(raw)
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
			v, err := Parse(raw)
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
			v, err := Parse([]byte(tc.raw))
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
