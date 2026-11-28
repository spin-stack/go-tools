package gate

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/spin-stack/go-tools/internal/gittest"
	"github.com/spin-stack/go-tools/versions"
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
		gittest.Run(t, root, args...)
	}
	return root
}

// A repository passes the gate when every pin is in versions.yaml, handed to its Dockerfiles with
// no default and read by something; and fails, saying where, on each way it can not be.
func TestTheGateSaysWhereAPinIsNotVersionsYAMLs(t *testing.T) {
	sha := strings.Repeat("c", 64)
	good := func() map[string]string {
		return map[string]string{
			versions.File: "- name: kernel\n  kind: download\n  source: https://x/{version}\n  version: '1'\n  pin: " + strings.Repeat("a", 64) +
				"\n  track: kernel\n- name: go\n  kind: image\n  source: golang\n  version: '1'\n  pin: sha256:" + strings.Repeat("b", 64) + "\n  track: go\n",
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
			f[versions.File] = strings.Replace(f[versions.File], "version: '1'\n  pin: sha256:", "version: >-\n    1\n  pin: sha256:", 1)
		}, want: []string{"go's version: not written on one line"}},
		{name: "an entry tracked with another, at its version in its own spelling", change: func(f map[string]string) {
			f[versions.File] += "- name: headers\n  kind: download\n  source: https://y/{version}\n  version: v1\n  pin: " + strings.Repeat("d", 64) + "\n  track: with kernel\n"
			f["Taskfile.yml"] += "  - versions args headers\n"
		}},
		{name: "an entry tracked with another repository's, which is check's to read", change: func(f map[string]string) {
			f[versions.File] += "- name: headers\n  kind: download\n  source: https://y/{version}\n  version: '9'\n  pin: " + strings.Repeat("d", 64) + "\n  track: with spin-stack/spin kernel\n"
			f["Taskfile.yml"] += "  - versions args headers\n"
		}},
		{name: "an entry tracked with another, moved alone", change: func(f map[string]string) {
			f[versions.File] += "- name: headers\n  kind: download\n  source: https://y/{version}\n  version: '2'\n  pin: " + strings.Repeat("d", 64) + "\n  track: with kernel\n"
			f["Taskfile.yml"] += "  - versions args headers\n"
		}, want: []string{`headers is with kernel, so it is at "1", not "2": versions bump kernel moves both`}},
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

// The gate finds a digest or a commit where the regexp it replaced did, and nowhere else.
func FuzzFirstPinIsTheRegexps(f *testing.F) {
	re := regexp.MustCompile(`\b[0-9a-f]{64}\b|\b[0-9a-f]{40}\b`)
	for _, s := range []string{
		"FROM golang@sha256:" + strings.Repeat("a", 64),
		"uses: actions/checkout@" + strings.Repeat("b", 40) + " # v5",
		strings.Repeat("c", 41), strings.Repeat("d", 64) + "x", "é" + strings.Repeat("e", 40),
		strings.Repeat("A", 40), "_" + strings.Repeat("f", 40), strings.Repeat("0", 40) + "-" + strings.Repeat("1", 64),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		if got, want := firstPin(line), re.FindString(line); got != want {
			t.Errorf("%q: firstPin %q, the regexp %q", line, got, want)
		}
	})
}

// A root git cannot list is refused in git's own words, not as a repository with nothing in it.
func TestAGateOutsideARepositorySaysWhatGitSaid(t *testing.T) {
	root := t.TempDir()
	body := "- name: go\n  kind: image\n  source: golang\n  version: '1'\n  pin: sha256:" + strings.Repeat("b", 64) + "\n  track: go\n"
	if err := os.WriteFile(filepath.Join(root, versions.File), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Gate{Root: root}.Check()
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("err = %v, want git's word that this is no repository", err)
	}
}

// A repository whose workflow or composite action uses the action that installs go-tools' task
// command reads versions.yaml's task through it, and owes none: the command carries its own. One
// that does not reads no task: not a go.mod that takes the command, which no repository runs, not
// a line that only names the action, and not a file outside .github that uses it.
func TestTheTaskCommandReadsTask(t *testing.T) {
	const task = "- name: task\n  kind: download\n  source: https://x/v{version}/task_{os}_{arch}.tar.gz\n  version: 3.54.0\n  pins:\n    linux/amd64: " +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n  track: github-release https://github.com/go-task/task\n"
	const uses = "      - uses: spin-stack/go-tools@0123456789012345678901234567890123456789 # v20261128.01\n"
	const ci = ".github/workflows/ci.yml"
	const unread = "pins task, and nothing reads it"
	for _, tc := range []struct {
		name, gomod, file string
		also              map[string]string
		says              []string
	}{
		{"the action and no task", "module x\n", "- name: go\n  kind: date\n  version: \"1\"\n  track: today\n", map[string]string{ci: uses},
			[]string{"pins go, and nothing reads it"}},
		{"nothing reads the task", "module x\n", task, nil, []string{unread}},
		{"a go.mod that takes the command", "module x\n\ntool github.com/spin-stack/go-tools/cmd/task\n", task, nil, []string{unread}},
		{"a workflow uses the action", "module x\n", task, map[string]string{ci: "jobs:\n  a:\n    steps:\n" + uses}, nil},
		{"a composite action uses it", "module x\n", task, map[string]string{".github/actions/setup/action.yml": "runs:\n  steps:\n    - uses: \"spin-stack/go-tools@main\"\n"}, nil},
		{"a workflow only names it", "module x\n", task, map[string]string{".github/workflows/ci.yml": "# spin-stack/go-tools@main installs task\n"}, []string{unread}},
		{"a page outside .github uses it", "module x\n", task, map[string]string{"docs/ci.md": uses}, []string{unread}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{versions.File: tc.file, "go.mod": tc.gomod}
			maps.Copy(files, tc.also)
			err := Gate{Root: repo(t, files)}.Check()
			if len(tc.says) == 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatalf("passed; want %q", tc.says)
			}
			for _, s := range tc.says {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("does not say %q:\n%v", s, err)
				}
			}
		})
	}
}

// A repository whose Taskfile includes go-tools' common tasks reads versions.yaml through them:
// the entry a task of theirs reads is read, though no line of the repository spells it, and a
// repository without that entry is told which task wants it. Mentioning the variable is no include.
func TestARepositoryThatIncludesTheCommonTasksOwesWhatTheyRead(t *testing.T) {
	const actionlint = "- name: actionlint\n  kind: module\n  source: github.com/rhysd/actionlint/cmd/actionlint\n  version: v1.7.12\n  track: github-release https://github.com/rhysd/actionlint\n"
	const other = "- name: go\n  kind: date\n  version: \"1\"\n  track: today\n"
	const reads = "tasks:\n  build:\n    cmds: [versions args go]\n"
	const unread = "pins actionlint, and nothing reads it"
	const owed = "Taskfile.yml includes go-tools' tasks, and its task lint:workflows reads actionlint: versions: there is no actionlint in versions.yaml"
	for _, tc := range []struct {
		name, taskfile, file string
		also                 map[string]string
		says                 string
	}{
		{name: "the include, and the entry its tasks read",
			taskfile: "includes:\n  common:\n    taskfile: '{{.GO_TOOLS_TASKS}}'\n    flatten: true\n", file: actionlint},
		{name: "the include as a path alone", taskfile: "includes:\n  common: '{{.GO_TOOLS_TASKS}}'\n", file: actionlint},
		{name: "the include with the template spaced", taskfile: "includes:\n  common: {taskfile: '{{ .GO_TOOLS_TASKS }}', flatten: true}\n", file: actionlint},
		{name: "the include after another", taskfile: "includes:\n  build: taskfiles/build.yml\n  common: {taskfile: '{{.GO_TOOLS_TASKS}}'}\n", file: actionlint},
		{name: "the include in a Taskfile the root one includes", taskfile: "includes:\n  lint: taskfiles/lint.yml\n", file: actionlint,
			also: map[string]string{"taskfiles/lint.yml": "includes:\n  common: {taskfile: '{{.GO_TOOLS_TASKS}}'}\n"}},
		{name: "the include, and no entry for its tasks", taskfile: "includes:\n  common: {taskfile: '{{.GO_TOOLS_TASKS}}', flatten: true}\n" + reads, file: other,
			says: owed},
		{name: "no include: nothing reads the entry", taskfile: reads, file: other + actionlint, says: unread},
		{name: "no include: nothing is owed", taskfile: reads, file: other},
		{name: "a comment that names the variable", taskfile: "# includes: {common: {taskfile: '{{.GO_TOOLS_TASKS}}'}}\n" + reads, file: other + actionlint,
			says: unread},
		{name: "another include, and the variable elsewhere",
			taskfile: "includes:\n  build: {taskfile: taskfiles/build.yml, vars: {SHARED: '{{.GO_TOOLS_TASKS}}'}}\n" + reads, file: other + actionlint, says: unread},
		{name: "a page that shows the include", taskfile: reads, file: other + actionlint,
			also: map[string]string{"README.md": "Take them:\n\n\tincludes:\n\t  common: {taskfile: '{{.GO_TOOLS_TASKS}}'}\n"}, says: unread},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{versions.File: tc.file, "Taskfile.yml": "version: '3'\n" + tc.taskfile}
			for rel, body := range tc.also {
				files[rel] = body
			}
			err := Gate{Root: repo(t, files)}.Check()
			switch {
			case tc.says == "" && err != nil:
				t.Fatal(err)
			case tc.says != "" && (err == nil || !strings.Contains(err.Error(), tc.says)):
				t.Errorf("the gate said %v, want %q", err, tc.says)
			case tc.says == owed && strings.Contains(err.Error(), "nothing reads"):
				t.Errorf("an entry the repository does read is called unread: %v", err)
			}
		})
	}
}

// What the common tasks read is read from their Taskfile: each task's reads, once each and in
// the tasks' order, whichever way the versions command is run; and a file that is no Taskfile is
// said, not taken as tasks that read nothing.
func TestWhatATaskReadsIsFoundInItsTaskfile(t *testing.T) {
	const taskfile = `version: '3'
tasks:
  lint:
    desc: Lint.
    cmds:
      - docker run $({{.VERSIONS}} ref golangci-lint)
      - cmd: go run x@$({{.GO_TOOLS}}versions version actionlint) && versions env go actionlint
  build:
    vars:
      GO: {sh: versions version go}
    cmds: [go build ./...]
  test:
    cmds: [go test ./...]
`
	got, err := taskReads([]byte(taskfile))
	want := []taskRead{{"build", "go"}, {"lint", "actionlint"}, {"lint", "go"}, {"lint", "golangci-lint"}}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("taskReads = %v, %v; want %v", got, err, want)
	}
	if got, err := taskReads([]byte("tasks: [")); err == nil || !strings.Contains(err.Error(), "go-tools' common tasks") {
		t.Errorf("of what is no Taskfile: %v, %v", got, err)
	}
	if errs := commonTasks(&versions.Versions{}, map[string]string{"Taskfile.yml": "includes: {common: '{{.GO_TOOLS_TASKS}}'}\n"}, []byte("tasks: ["), map[string]bool{}); len(errs) != 1 {
		t.Errorf("a repository that includes tasks that do not read is told so once, not %v", errs)
	}
}
