package tasks

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// taskExe is the Task these tests run tasks.yml under: the one `task test` runs under, which
// the Taskfile hands down as TASK_EXE - the version versions.yaml pins, as in every repository.
func taskExe(t *testing.T) string {
	t.Helper()
	exe := os.Getenv("TASK_EXE")
	if exe == "" {
		t.Fatal("TASK_EXE names no Task: these tests run tasks.yml under Task itself, so run them with `task test`")
	}
	return exe
}

// stubs is a directory of the commands tasks.yml calls, each a program that writes its name and
// arguments as a line of log and answers what a task reads from it. `versions check` fails for
// a file named behind.yaml, and `proven` when REFUSE is set.
func stubs(t *testing.T) (bin, log string) {
	t.Helper()
	bin, log = t.TempDir(), filepath.Join(t.TempDir(), "log")
	for name, answer := range map[string]string{
		"versions":  `case "$*" in "version actionlint") echo v1.7.12 ;; *behind.yaml) exit 1 ;; esac`,
		"proven":    `[ -z "${REFUSE:-}" ]`,
		"release":   `echo v20261122.01`,
		"consumers": ``,
		"go":        ``,
		"git":       `case "$1" in rev-parse) echo 0123abc ;; esac`,
		"gh":        `case "$1" in auth) echo a-token ;; repo) echo o/r ;; esac`,
	} {
		script := "#!/bin/sh\necho \"" + name + " $*\" >> '" + log + "'\n" + answer + "\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil { //nolint:gosec // a program the test runs
			t.Fatal(err)
		}
	}
	return bin, log
}

// fixture is a repository: a directory with files, among them a Taskfile.yml, and the stubs first on PATH.
type fixture struct {
	t        *testing.T
	dir, log string
	env      []string
}

// including is a Taskfile that takes tasks.yml as a repository does, with vars as the include's
// and rest after it.
func including(vars, rest string) string {
	return "version: '3'\nincludes:\n  common:\n    taskfile: '{{.GO_TOOLS_TASKS}}'\n    flatten: true\n    vars: {" + vars + "}\n" + rest
}

func newRepository(t *testing.T, files map[string]string) fixture {
	t.Helper()
	bin, log := stubs(t)
	tasks, err := filepath.Abs("tasks.yml")
	if err != nil {
		t.Fatal(err)
	}
	r := fixture{t: t, dir: t.TempDir(), log: log, env: []string{
		"PATH=" + bin + string(os.PathListSeparator) + "/usr/bin" + string(os.PathListSeparator) + "/bin",
		"GO_TOOLS_TASKS=" + tasks, "HOME=" + t.TempDir(),
	}}
	for name, content := range files {
		path := filepath.Join(r.dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

// task runs Task with args in the repository's directory sub, and answers what it printed,
// whether it passed, and the commands the tasks ran.
func (r fixture) task(sub string, args ...string) (out string, ok bool, ran []string) {
	r.t.Helper()
	_ = os.Remove(r.log)                                             //nolint:errcheck // absent before the first run
	cmd := exec.CommandContext(r.t.Context(), taskExe(r.t), args...) //nolint:gosec // the Task `task test` runs under
	cmd.Dir, cmd.Env = filepath.Join(r.dir, sub), r.env
	said, err := cmd.CombinedOutput()
	if logged, err := os.ReadFile(r.log); err == nil {
		ran = strings.Split(strings.TrimSuffix(string(logged), "\n"), "\n")
	}
	return string(said), err == nil, ran
}

func (r fixture) ran(want []string, args ...string) {
	r.t.Helper()
	out, ok, ran := r.task("", args...)
	if !ok || strings.Join(ran, "\n") != strings.Join(want, "\n") {
		r.t.Errorf("task %v: passed=%t and ran:\n  %s\nwant:\n  %s\n%s", args, ok, strings.Join(ran, "\n  "), strings.Join(want, "\n  "), out)
	}
}

func (r fixture) refused(says string, args ...string) (ran []string) {
	r.t.Helper()
	out, ok, ran := r.task("", args...)
	if ok || !strings.Contains(out, says) {
		r.t.Errorf("task %v: passed=%t, want a refusal saying %q:\n%s", args, ok, says, out)
	}
	return ran
}

// A repository whose Taskfile is the include and nothing else has every common task, by its own
// name and with its description, from any directory of it.
func TestARepositoryThatIncludesTheTasksHasThem(t *testing.T) {
	r := newRepository(t, map[string]string{"Taskfile.yml": including("", ""), "versions.yaml": "", "internal/x/x.go": ""})
	out, ok, _ := r.task("internal/x", "--list-all", "--json", "--no-status")
	if !ok {
		t.Fatal(out)
	}
	var listed struct {
		Tasks []struct {
			Name string `json:"name"`
			Desc string `json:"desc"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatal(err)
	}
	has := map[string]bool{}
	for _, task := range listed.Tasks {
		has[task.Name] = task.Desc != ""
	}
	for _, name := range []string{"versions", "bump", "release", "lint:workflows", "consumers"} {
		if !has[name] {
			t.Errorf("no task %s with a description, in: %v", name, has)
		}
	}
	if len(listed.Tasks) != 5 {
		t.Errorf("%d tasks, and the repository wrote none: %v", len(listed.Tasks), has)
	}

	if out, ok, ran := r.task("internal/x", "versions"); !ok || len(ran) != 1 || ran[0] != "versions check -file versions.yaml" {
		t.Errorf("from a directory of the repository, task versions passed=%t and ran %v\n%s", ok, ran, out)
	}
	r.ran([]string{"versions bump -file versions.yaml golangci-lint"}, "bump", "NAME=golangci-lint")
	r.ran([]string{"versions bump -file cmd/sfw/versions.yaml socket v1.2.0"}, "bump", "NAME=socket", "TO=v1.2.0", "FILE=cmd/sfw/versions.yaml")
	if ran := r.refused("missing required variables: NAME", "bump"); len(ran) != 0 {
		t.Errorf("a bump of nothing ran %v", ran)
	}
}

// Every file of pins is asked, whichever is behind, and one behind fails the task.
func TestVersionsAsksEveryFileOfPins(t *testing.T) {
	r := newRepository(t, map[string]string{"Taskfile.yml": including("VERSIONS_FILES: versions.yaml behind.yaml cmd/sfw/versions.yaml", "")})
	ran := r.refused("exit status 1", "versions")
	if want := "versions check -file versions.yaml\nversions check -file behind.yaml\nversions check -file cmd/sfw/versions.yaml"; strings.Join(ran, "\n") != want {
		t.Errorf("ran:\n%s\nwant:\n%s", strings.Join(ran, "\n"), want)
	}
}

// A repository's parameters reach release: its gate's workflow and jobs are what proven is
// asked, and its files are on the release - of main's head, or of the commit named.
func TestReleaseIsOfTheRepositorysGateAndFiles(t *testing.T) {
	r := newRepository(t, map[string]string{"Taskfile.yml": including("GATE_WORKFLOW: pages.yml, GATE_JOBS: 'Gate,Site', RELEASE_FILES: _output/a.tar.gz _output/checksums.txt", "")})
	asked := "proven release -repo o/r -sha 0123abc -workflow pages.yml -events push,pull_request -ran Gate,Site -also "
	r.ran([]string{
		"git fetch --quiet --tags origin main", "gh auth token", "git rev-parse --verify origin/main^{commit}",
		"gh repo view --json nameWithOwner --jq .nameWithOwner", asked, "release 0123abc",
		"gh release create v20261122.01 _output/a.tar.gz _output/checksums.txt --repo o/r --target 0123abc --title v20261122.01 --latest --generate-notes",
	}, "release")

	// In a workflow: the token and the repository are the run's, and the commit the one it names.
	r.env = append(r.env, "GH_TOKEN=the-runs", "GITHUB_REPOSITORY=spin-stack/go-tools")
	r.ran([]string{
		"git fetch --quiet --tags origin main", "git rev-parse --verify 4567def^{commit}",
		"proven release -repo spin-stack/go-tools -sha 0123abc -workflow pages.yml -events push,pull_request -ran Gate,Site -also ", "release 0123abc",
		"gh release create v20261122.01 _output/a.tar.gz _output/checksums.txt --repo spin-stack/go-tools --target 0123abc --title v20261122.01 --latest --generate-notes",
	}, "release", "SHA=4567def")

	// A commit the gate has not passed on is not named and not released.
	r.env = append(r.env, "REFUSE=1")
	if ran := r.refused("exit status 1", "release"); ran[len(ran)-1] != asked[:len("proven release -repo ")]+"spin-stack/go-tools"+asked[len("proven release -repo o/r"):] {
		t.Errorf("after proven refused, the task went on: %v", ran)
	}
}

// A repository that names no gate does not release, and is told so before anything runs.
func TestARepositoryWithNoGateDoesNotRelease(t *testing.T) {
	r := newRepository(t, map[string]string{"Taskfile.yml": including("", "")})
	if ran := r.refused("missing required variables: GATE_WORKFLOW, GATE_JOBS", "release"); len(ran) != 0 {
		t.Errorf("ran %v", ran)
	}
}

// A repository that defines a common name itself is refused by Task, whatever task is asked for:
// the name has one meaning.
func TestARepositoryThatRedefinesACommonTaskIsRefused(t *testing.T) {
	r := newRepository(t, map[string]string{"Taskfile.yml": including("", "tasks:\n  own:\n    cmds: [echo own]\n  versions:\n    cmds: [versions check]\n")})
	for _, args := range [][]string{{"versions"}, {"own"}, {"--list-all"}} {
		if ran := r.refused("Found multiple tasks (versions)", args...); len(ran) != 0 {
			t.Errorf("task %v ran %v", args, ran)
		}
	}
}

// The workflows are read by actionlint at the repository's pin, and an action used by a tag
// fails the task: in .github, and in a second directory the repository names.
func TestLintWorkflowsHoldsEveryActionToACommit(t *testing.T) {
	const pinned = "    steps:\n      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1\n      - uses: ./.github/actions/own\n"
	const tagged = "    steps:\n      - uses: actions/checkout@v7\n"
	const lint = "go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -shellcheck="
	r := newRepository(t, map[string]string{"Taskfile.yml": including("", ""), ".github/workflows/ci.yml": pinned})
	r.ran([]string{"versions version actionlint", lint}, "lint:workflows")

	also := newRepository(t, map[string]string{"Taskfile.yml": including("WORKFLOWS_ALSO: site/example/.github", ""),
		".github/workflows/ci.yml": pinned, "site/example/.github/workflows/ci.yml": pinned})
	also.ran([]string{"versions version actionlint", lint + " .github/workflows/ci.yml site/example/.github/workflows/ci.yml"}, "lint:workflows")

	for name, files := range map[string]map[string]string{
		"in .github":          {"Taskfile.yml": including("", ""), ".github/workflows/ci.yml": tagged},
		"in the second place": {"Taskfile.yml": including("WORKFLOWS_ALSO: site/example/.github", ""), ".github/workflows/ci.yml": pinned, "site/example/.github/workflows/ci.yml": tagged},
	} {
		t.Run(name, func(t *testing.T) {
			newRepository(t, files).refused("actions/checkout@v7\nan action above is used by a tag its owner can move", "lint:workflows")
		})
	}
}

// consumers reads the repository's table, where it says it is, with what follows --; a
// repository with no table is told it has none.
func TestConsumersReadsTheRepositorysTable(t *testing.T) {
	r := newRepository(t, map[string]string{"Taskfile.yml": including("", ""), "consumers.toml": ""})
	r.ran([]string{"git fetch --quiet --tags origin main", "consumers -table consumers.toml -checkout o/site=../site"},
		"consumers", "--", "-checkout", "o/site=../site")

	moved := newRepository(t, map[string]string{"Taskfile.yml": including("CONSUMERS: docs/consumers.toml", ""), "docs/consumers.toml": ""})
	moved.ran([]string{"git fetch --quiet --tags origin main", "consumers -table docs/consumers.toml"}, "consumers")

	none := newRepository(t, map[string]string{"Taskfile.yml": including("", "")})
	if ran := none.refused("no consumers.toml: this repository names nobody that copies from it", "consumers"); len(ran) != 0 {
		t.Errorf("ran %v", ran)
	}
}

// go-tools sets GO_TOOLS, and each command is then its tree's.
func TestGoToolsRunsItsTreesCommands(t *testing.T) {
	r := newRepository(t, map[string]string{"Taskfile.yml": including("GO_TOOLS: 'go run ./cmd/'", ""), "consumers.toml": ""})
	r.ran([]string{"go run ./cmd/versions check -file versions.yaml"}, "versions")
	r.ran([]string{"go run ./cmd/versions bump -file versions.yaml task"}, "bump", "NAME=task")
	r.ran([]string{"git fetch --quiet --tags origin main", "go run ./cmd/consumers -table consumers.toml"}, "consumers")
}

// This repository's own Taskfile takes the common tasks from tasks.yml and writes none of them.
func TestThisRepositoryTakesTheTasksItShips(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), taskExe(t), "--dir", "../..", "--list-all", "--json", "--no-status") //nolint:gosec // the Task `task test` runs under
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Tasks []struct {
			Name     string `json:"name"`
			Location struct {
				Taskfile string `json:"taskfile"`
			} `json:"location"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		t.Fatal(err)
	}
	from := map[string]string{}
	for _, task := range listed.Tasks {
		from[task.Name] = filepath.Base(task.Location.Taskfile)
	}
	for _, name := range []string{"versions", "bump", "release", "lint:workflows", "consumers"} {
		if from[name] != "tasks.yml" {
			t.Errorf("task %s is from %q, want tasks.yml", name, from[name])
		}
	}
}
