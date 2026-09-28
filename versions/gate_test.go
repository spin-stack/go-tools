package versions

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repo is a repository of files, rel to body.
func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return root
}

// A repository passes the gate when every pin is in versions.yaml, handed to its Dockerfiles with
// no default and read by something; and fails, saying where, on each way it can not be.
func TestTheGateSaysWhereAPinIsNotVersionsYAMLs(t *testing.T) {
	sha := strings.Repeat("c", 64)
	good := func() map[string]string {
		return map[string]string{
			File: "- name: kernel\n  kind: download\n  source: https://x/{version}\n  version: '1'\n  pin: " + strings.Repeat("a", 64) +
				"\n  track: kernel-stable\n- name: go\n  kind: image\n  source: golang\n  version: '1'\n  pin: sha256:" + strings.Repeat("b", 64) + "\n  track: go\n",
			"Taskfile.yml":      "cmds:\n  - docker build $(go tool versions args kernel) .\n  - '{{.VERSIONS}} ref go'\n",
			"kernel/Dockerfile": "ARG KERNEL_VERSION\nARG KERNEL_SHA256\nFROM scratch\n",
			// Written by other tools, or by a build: not the gate's.
			"go.sum":                    "x v1 h1:" + sha + "\n",
			"_output/manifest.json":     sha,
			".cache/x":                  sha,
			"x_test.go":                 sha,
			".github/workflows/ci.yml":  "steps:\n  - uses: actions/checkout@" + strings.Repeat("d", 40) + " # v5\n",
			"records/measurements.json": sha,
			// What git ignores is nobody's pin: a dependency tree, a build's binaries.
			".gitignore":              "node_modules/\n",
			"node_modules/x/index.js": sha,
			// A composite action's own steps are Dependabot's too.
			".github/actions/setup/action.yml": "runs:\n  steps:\n    - uses: actions/cache@" + strings.Repeat("e", 40) + " # v6\n",
		}
	}
	for _, tc := range []struct {
		name   string
		change func(map[string]string)
		reads  []*regexp.Regexp
		want   []string
	}{
		{name: "every pin in its place"},
		{name: "a digest written elsewhere", change: func(f map[string]string) { f["README.md"] = "the kernel is\n" + sha + "\n" },
			want: []string{"README.md:2 pins " + sha}},
		{name: "a step that runs an action by its tag's commit, not as a uses:",
			change: func(f map[string]string) {
				f[".github/workflows/ci.yml"] += "  - run: git checkout " + strings.Repeat("d", 40) + "\n"
			},
			want: []string{".github/workflows/ci.yml:3 pins"}},
		{name: "an argument with a default", change: func(f map[string]string) { f["kernel/Dockerfile"] = "ARG KERNEL_VERSION=1\nARG KERNEL_SHA256\n" },
			want: []string{"ARG KERNEL_VERSION has a default"}},
		{name: "an argument no entry has", change: func(f map[string]string) { f["kernel/Dockerfile"] += "ARG QEMU_VERSION\n" },
			want: []string{"ARG QEMU_VERSION is no entry's"}},
		{name: "an entry nothing reads", change: func(f map[string]string) { f["Taskfile.yml"] = "cmds: [true]\n" },
			want: []string{"pins kernel, and nothing reads it", "pins go, and nothing reads it"}},
		{name: "an entry a bump cannot rewrite in place", change: func(f map[string]string) {
			f[File] = strings.Replace(f[File], "version: '1'\n  pin: sha256:", "version: >-\n    1\n  pin: sha256:", 1)
		}, want: []string{"go's version: not written on one line"}},
		{name: "a read of no entry", change: func(f map[string]string) { f["Taskfile.yml"] += "  - versions version rust\n" },
			want: []string{"Taskfile.yml reads rust"}},
		{name: "a test's fixture is not a pin, and what it reads is read",
			change: func(f map[string]string) {
				f["Taskfile.yml"] = "cmds:\n  - versions args kernel\n"
				f["store_test.go"] = "const other = \"" + sha + "\"\nvar image = versions.MustGet(\"go\")\n"
			},
			reads: []*regexp.Regexp{regexp.MustCompile(`versions args((?: [a-z-]+)+)`), regexp.MustCompile(`\.MustGet\("([a-z0-9-]+)"\)`)}},
		{name: "a repository's own way of reading", change: func(f map[string]string) { f["main.go"] = `v.Get("go")` },
			reads: []*regexp.Regexp{regexp.MustCompile(`versions args((?: [a-z-]+)+)`), regexp.MustCompile(`\.Get\("([a-z0-9-]+)"\)`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := good()
			if tc.change != nil {
				tc.change(files)
			}
			err := Gate{Root: repo(t, files), Elsewhere: []string{"go.sum", "records/"}, Reads: tc.reads}.Check()
			if len(tc.want) == 0 && err != nil {
				t.Fatalf("the gate failed: %v", err)
			}
			for _, w := range tc.want {
				if err == nil || !strings.Contains(err.Error(), w) {
					t.Errorf("the gate said %v, want %q", err, w)
				}
			}
		})
	}
}
