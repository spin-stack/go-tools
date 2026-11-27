package taskgate_test

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/internal/gittest"
	"github.com/spin-stack/go-tools/internal/taskgate"
)

// held is a repository as every one that has the common tasks has them: go.mod takes `go tool
// task` at a go-tools that names the tasks, the root Taskfile includes them once and flattened,
// says it does not release, and the task CI runs reaches lint:workflows.
func held() map[string]string {
	return map[string]string{
		"go.mod": "module example.com/r\n\ngo 1.27\n\ntool github.com/spin-stack/go-tools/cmd/task\n\nrequire github.com/spin-stack/go-tools v0.20261122.6\n",
		"Taskfile.yml": `version: '3'
includes:
  common:
    taskfile: "{{.GO_TOOLS_TASKS}}"
    flatten: true
    vars:
      RELEASE:
        map: {none: "its outputs are copied at a commit"}
tasks:
  lint:
    cmds:
      - task: lint:workflows
`,
		".github/workflows/ci.yml": "on: pull_request\njobs:\n  gate:\n    name: Gate\n    runs-on: ubuntu-latest\n    steps:\n      - run: go tool task lint\n",
	}
}

// common is what Common finds in a repository of files, each finding as its key and what it says.
func common(t *testing.T, files map[string]string) (map[string]string, string) {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte(body), 0o600))
	}
	gittest.Run(t, root, "init", "-q")
	findings, said, err := taskgate.Common(t.Context(), root, taskgate.CommonInclude)
	require.NoError(t, err)
	got := map[string]string{}
	for _, f := range findings {
		got[f.Key()] = f.String()
	}
	return got, said
}

// with is held, with files replaced, added, or removed by an empty body.
func with(files map[string]string) map[string]string {
	out := held()
	for rel, body := range files {
		if body == "" {
			delete(out, rel)
			continue
		}
		out[rel] = body
	}
	return out
}

// taskfile is held's Taskfile with the include's vars and the tasks given.
func taskfile(include, tasks string) string {
	return "version: '3'\nincludes:\n" + include + "tasks:\n" + tasks
}

const (
	includeNone = "  common:\n    taskfile: \"{{.GO_TOOLS_TASKS}}\"\n    flatten: true\n    vars:\n      RELEASE:\n        map: {none: \"its outputs are copied\"}\n"
	lint        = "  lint:\n    desc: What CI runs.\n    cmds:\n      - task: lint:workflows\n"
)

func TestARepositoryThatHasTheCommonTasksPasses(t *testing.T) {
	got, said := common(t, held())
	assert.Empty(t, got)
	assert.Equal(t, "the common tasks: included once and flattened, none of their names the repository's own, RELEASE says none, and lint:workflows is in what a workflow runs", said)

	// go-tools' own: no `go tool task`, and the include names the tree's file, which is not read
	// for the repository's tasks.
	files := with(map[string]string{"go.mod": "module github.com/spin-stack/go-tools\n\ngo 1.27\n"})
	files[".github/workflows/ci.yml"] = "on: push\njobs:\n  gate:\n    steps:\n      - run: task lint\n"
	root := t.TempDir()
	for rel, body := range files {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte(strings.ReplaceAll(body, "{{.GO_TOOLS_TASKS}}", "internal/tasks/tasks.yml")), 0o600))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(root, "internal/tasks"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, "internal/tasks/tasks.yml"), []byte("version: '3'\ntasks:\n  versions:\n    cmds: [go run ./cmd/versions check]\n"), 0o600))
	gittest.Run(t, root, "init", "-q")
	findings, _, err := taskgate.Common(t.Context(), root, "internal/tasks/tasks.yml")
	require.NoError(t, err)
	assert.Empty(t, findings, "a bare `task` in a repository that does not take `go tool task`, or the included file's own tasks")
}

// Of a repository with no `go tool task`, no versions.yaml and no include nothing is asked, and
// it is said so. One that pins in versions.yaml is asked for the include, as one that takes
// `go tool task` is, and told how.
func TestOnlyARepositoryThatRunsGoToolTaskOrPinsIsAsked(t *testing.T) {
	got, said := common(t, map[string]string{"go.mod": "module example.com/r\n", "Taskfile.yml": "version: '3'\ntasks:\n  release:\n    cmds: [make]\n"})
	assert.Empty(t, got)
	assert.Equal(t, "the common tasks: not asked of this repository, whose go.mod does not take github.com/spin-stack/go-tools/cmd/task, which has no versions.yaml, and whose Taskfile does not include them", said)

	got, _ = common(t, map[string]string{"go.mod": "module example.com/r\n", "Taskfile.yml": "version: '3'\ntasks:\n  x:\n    cmds: [make]\n", "versions.yaml": "[]\n"})
	assert.Equal(t, []string{"common:include"}, slices.Sorted(maps.Keys(got)))
	assert.Contains(t, got["common:include"], "Taskfile.yml: versions.yaml pins")
	assert.Contains(t, got["common:include"], "`go get -tool github.com/spin-stack/go-tools/cmd/task`")

	got, _ = common(t, with(map[string]string{"Taskfile.yml": "", "versions.yaml": "[]\n"}))
	assert.Equal(t, []string{"common:include"}, slices.Sorted(maps.Keys(got)))
	assert.Equal(t, "Taskfile.yml: go.mod takes github.com/spin-stack/go-tools/cmd/task, and this Taskfile does not include go-tools' common tasks. "+
		"Add to its includes `common: {taskfile: \"{{.GO_TOOLS_TASKS}}\", flatten: true, vars: {RELEASE: {map: {none: <why it does not release>}}}}`, "+
		"with RELEASE as the repository is released, and delete or rename its own tasks of those names", got["common:include"])
}

// The include is the root Taskfile's, flattened, and whole: the names are `versions` and not
// `common:versions`, and no task of the repository's own answers to one of them.
func TestTheIncludeIsTheRootsFlattenedAndWhole(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]string
		want  []string
		says  string
	}{
		"not flattened": {
			files: map[string]string{"Taskfile.yml": taskfile(strings.Replace(includeNone, "flatten: true", "flatten: false", 1), lint)},
			want:  []string{"common:include"},
			says:  "Taskfile.yml:3: go-tools' common tasks are included under the names common:<task>, from Taskfile.yml",
		},
		"a path and no more": {
			files: map[string]string{"Taskfile.yml": taskfile("  common: \"{{ .GO_TOOLS_TASKS }}\"\n", lint)},
			want:  []string{"common:RELEASE", "common:include"},
			says:  "states no RELEASE",
		},
		"from a file the root includes": {
			files: map[string]string{
				"Taskfile.yml":       taskfile("  more:\n    taskfile: taskfiles/more.yml\n    flatten: true\n", lint),
				"taskfiles/more.yml": "version: '3'\nincludes:\n" + includeNone,
			},
			want: []string{"common:include"},
			says: "included under the names <task>, from taskfiles/more.yml",
		},
		"with excludes": {
			files: map[string]string{"Taskfile.yml": taskfile(includeNone+"    excludes: [release]\n", lint)},
			want:  []string{"common:excludes"},
			says:  "Taskfile.yml:3: the include of go-tools' common tasks has `excludes`",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, _ := common(t, with(tc.files))
			assert.Equal(t, tc.want, slices.Sorted(maps.Keys(got)))
			assert.Contains(t, got[tc.want[0]], tc.says)
		})
	}
}

// A go.mod whose go-tools is from before its `go tool task` named the tasks makes the include a
// cycle for every task: it is refused, and the command that moves it said.
func TestAGoToolsFromBeforeTheTasksIsAFinding(t *testing.T) {
	for require, refused := range map[string]bool{
		"require github.com/spin-stack/go-tools v0.20261122.3\n":                              true,
		"require (\n\tgithub.com/spin-stack/go-tools v0.0.0-20261109214710-b4b5f4a78a45\n)\n": true,
		"require github.com/spin-stack/go-tools v0.20261122.4\n":                              false,
		"require (\n\tgithub.com/spin-stack/go-tools v0.20261123.1 // indirect\n)\n":          false,
		"": false,
	} {
		got, _ := common(t, with(map[string]string{"go.mod": "module example.com/r\n\ntool github.com/spin-stack/go-tools/cmd/task\n\n" + require}))
		if !refused {
			assert.Empty(t, got, require)
			continue
		}
		assert.Equal(t, []string{"common:go-tools"}, slices.Sorted(maps.Keys(got)), require)
		assert.Contains(t, got["common:go-tools"], "go.mod: requires github.com/spin-stack/go-tools v0.")
		assert.Contains(t, got["common:go-tools"], "from before v0.20261122.4")
		assert.Contains(t, got["common:go-tools"], "`go get github.com/spin-stack/go-tools@latest && go mod tidy`")
	}
}

// A task of the repository's own whose name Task knows as a common one is found in the file that
// writes it: the root's, or one an include names, under the include's namespace. Task's own
// error for it names neither and stops every task.
func TestACommonNameOfTheRepositorysOwnIsFound(t *testing.T) {
	got, _ := common(t, with(map[string]string{
		"Taskfile.yml": taskfile(includeNone+
			"  lint: {taskfile: taskfiles/lint.yml}\n"+
			"  build:\n    taskfile: taskfiles/build\n    flatten: true\n"+
			"  gone: {taskfile: taskfiles/gone.yml}\n"+
			"  made: {taskfile: \"{{.DIR}}/x.yml\"}\n"+
			"  empty: {taskfile: taskfiles/empty.yml}\n",
			lint+"  versions:\n    cmds: [make versions]\n  release:check:\n    cmds: [make]\n"),
		"taskfiles/lint.yml":  "version: '3'\nincludes:\n  again: {taskfile: lint.yml}\ntasks:\n  workflows:\n    cmds: [actionlint]\n  versions:\n    cmds: [make]\n",
		"taskfiles/empty.yml": "# nothing yet\n",
		// A directory's Taskfile, flattened: its bump is `bump`.
		"taskfiles/build/Taskfile.yml": "version: '3'\nincludes:\n  up: {taskfile: ../../Taskfile.yml}\ntasks:\n  bump:\n    cmds: [make]\n",
	}))
	assert.Equal(t, []string{"common:Taskfile.yml:versions", "common:taskfiles/build/Taskfile.yml:bump", "common:taskfiles/lint.yml:workflows"}, slices.Sorted(maps.Keys(got)))
	assert.Equal(t, "Taskfile.yml:21: task versions is one of go-tools' common tasks, which the include defines: delete it, or rename it after what it does if it does something else "+
		"(with both Task refuses every task: `Found multiple tasks (versions)`)", got["common:Taskfile.yml:versions"])
	assert.Equal(t, "taskfiles/lint.yml:5: task workflows is lint:workflows through the include `lint`, and lint:workflows is one of go-tools' common tasks: rename it in taskfiles/lint.yml, "+
		"after what it does (with both Task refuses every task: `Found multiple tasks (lint:workflows) included by \"lint\"\"`)", got["common:taskfiles/lint.yml:workflows"])
	assert.Contains(t, got["common:taskfiles/build/Taskfile.yml:bump"], "task bump is bump through the include `build`")
}

// In a repository that takes `go tool task`, a step that runs Task is that one: a bare `task` is
// whatever Task the runner has, which sets no GO_TOOLS_TASKS, and setup-task installs one.
func TestAStepRunsTheTaskGoModTakes(t *testing.T) {
	got, _ := common(t, with(map[string]string{".github/workflows/ci.yml": `on: push
jobs:
  gate:
    steps:
      - uses: go-task/setup-task@0123456789012345678901234567890123456789 # v1
      - uses: go-task/setup-task
        id: again
      - uses: actions/setup-go@0123456789012345678901234567890123456789 # v6
      - run: go tool task lint
      - name: Test
        run: task test
      - run: |
          go version
          task
      - run: tasks
`, ".github/actions/setup/action.yml": "runs:\n  using: composite\n  steps:\n    - run: task setup\n      shell: bash\n"}))
	assert.Equal(t, []string{
		"common:.github/actions/setup/action.yml/#1/task",
		"common:.github/workflows/ci.yml/gate/#1/setup-task",
		"common:.github/workflows/ci.yml/gate/#6/task",
		"common:.github/workflows/ci.yml/gate/Test/task",
		"common:.github/workflows/ci.yml/gate/again/setup-task",
	}, slices.Sorted(maps.Keys(got)))
	assert.Equal(t, ".github/workflows/ci.yml:11: step \"Test\" runs `task test`: write `go tool task test`. "+
		"A bare `task` is whatever Task the runner has, with no GO_TOOLS_TASKS, and stops at `include cycle detected`", got["common:.github/workflows/ci.yml/gate/Test/task"])
	assert.Contains(t, got["common:.github/workflows/ci.yml/gate/#1/setup-task"], ".github/workflows/ci.yml:5: step \"#1\" installs Task with go-task/setup-task")
}

// lint:workflows is reached from what a workflow runs, by any way a task runs another: a `task:`
// of its cmds, a dep, a line of its shell that is Task, from a namespace's file too. Nothing runs
// a common task unless the repository's own do.
func TestLintWorkflowsIsInWhatAWorkflowRuns(t *testing.T) {
	step := func(run string) string {
		return "on: push\njobs:\n  gate:\n    steps:\n      - run: " + run + "\n"
	}
	for name, tc := range map[string]struct {
		run, tasks string
		files      map[string]string
		unreached  bool
	}{
		"a step":                    {run: "go tool task lint:workflows"},
		"a step, of several":        {run: "go tool task test lint:workflows"},
		"a task:":                   {run: "go tool task lint", tasks: lint},
		"through two tasks":         {run: "go tool task check", tasks: "  check:\n    cmds:\n      - task: lint\n" + lint},
		"a dep":                     {run: "go tool task lint", tasks: "  lint:\n    deps: [lint:workflows]\n"},
		"a dep with vars":           {run: "go tool task lint", tasks: "  lint:\n    deps:\n      - task: lint:workflows\n        vars: {X: y}\n"},
		"Task in the shell":         {run: "go tool task lint", tasks: "  lint:\n    cmds:\n      - \"{{.TASK_EXE}} lint:workflows\"\n"},
		"go tool task in the shell": {run: "go tool task lint", tasks: "  lint:\n    cmds:\n      - go vet ./...\n      - go tool task lint:workflows\n"},
		"a namespace's task, by the root's name": {run: "go tool task ci:lint", files: map[string]string{
			"taskfiles/ci.yml": "version: '3'\ntasks:\n  lint:\n    cmds:\n      - task: :lint:workflows\n",
		}},
		"a namespace's task, by its own": {run: "go tool task ci:all", files: map[string]string{
			"taskfiles/ci.yml": "version: '3'\ntasks:\n  all:\n    cmds:\n      - task: lint\n  lint:\n    cmds:\n      - task: :lint:workflows\n",
		}},
		"no task that reaches it": {run: "go tool task lint", tasks: "  lint:\n    cmds:\n      - go vet ./...\n      - task: test\n  test:\n    cmds: [go test ./...]\n", unreached: true},
		"a name a template makes": {run: "go tool task lint", tasks: "  lint:\n    cmds:\n      - task: \"{{.WHICH}}\"\n", unreached: true},
		"a namespace's task of the same name": {run: "go tool task ci:all", files: map[string]string{
			"taskfiles/ci.yml": "version: '3'\ntasks:\n  all:\n    cmds:\n      - task: lint\n  lint:\n    cmds: [go vet ./...]\n",
		}, tasks: lint, unreached: true},
		"a task no workflow runs": {run: "go tool task test", tasks: lint + "  test:\n    cmds: [go test ./...]\n", unreached: true},
	} {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{
				"Taskfile.yml":             taskfile(includeNone+"  ci: {taskfile: taskfiles/ci.yml}\n", tc.tasks),
				".github/workflows/ci.yml": step(tc.run),
			}
			maps.Copy(files, tc.files)
			got, _ := common(t, with(files))
			if !tc.unreached {
				assert.Empty(t, got)
				return
			}
			assert.Equal(t, []string{"common:lint:workflows"}, slices.Sorted(maps.Keys(got)))
			assert.Equal(t, "Taskfile.yml:3: lint:workflows is run by no task a workflow runs, so nothing holds the workflows to actionlint and to actions pinned by commit: "+
				"add `- task: lint:workflows` to the cmds of the task CI runs (lint, check or gate) in Taskfile.yml", got["common:lint:workflows"])
		})
	}

	got, _ := common(t, with(map[string]string{".github/workflows/ci.yml": ""}))
	assert.Empty(t, got, "a repository with no workflow has nothing for it to read")
}

// A job's runner label is one actionlint knows - GitHub's, or one .github/actionlint.yaml names -
// or lint:workflows fails on it. Each unknown label is said once, where it is first used.
func TestARunnerLabelIsOneActionlintKnows(t *testing.T) {
	const workflow = `on: push
jobs:
  gate:
    runs-on: runs-on/fleet=docker8/env=production
    steps:
      - run: go tool task lint
  again:
    runs-on: [self-hosted, runs-on/fleet=docker8/env=production, linux, arm64]
  kvm:
    runs-on:
      group: fleet
      labels: [runs-on/fleet=kvm/env=production]
  grouped:
    runs-on:
      group: fleet
  hosted:
    runs-on: ubuntu-24.04-arm
  chosen:
    runs-on: ${{ inputs.runner }}
`
	got, _ := common(t, with(map[string]string{".github/workflows/ci.yml": workflow}))
	assert.Equal(t, []string{"common:label:runs-on/fleet=docker8/env=production", "common:label:runs-on/fleet=kvm/env=production"}, slices.Sorted(maps.Keys(got)))
	assert.Equal(t, ".github/workflows/ci.yml:4: job gate runs on \"runs-on/fleet=docker8/env=production\", which is not one of GitHub's labels and .github/actionlint.yaml does not name: "+
		"lint:workflows fails on it. Write in .github/actionlint.yaml `self-hosted-runner: {labels: [runs-on/fleet=docker8/env=production]}`, with a comment that says whose runners those are",
		got["common:label:runs-on/fleet=docker8/env=production"])
	assert.Contains(t, got["common:label:runs-on/fleet=kvm/env=production"], ".github/workflows/ci.yml:12: job kvm runs on")

	for file, labels := range map[string]string{
		".github/actionlint.yaml": "self-hosted-runner:\n  labels:\n    - runs-on/fleet=docker8/env=production\n    - runs-on/fleet=kvm/env=production\n",
		".github/actionlint.yml":  "self-hosted-runner:\n  labels: [\"runs-on/fleet=*/env=production\"]\n",
	} {
		got, _ := common(t, with(map[string]string{".github/workflows/ci.yml": workflow, file: labels}))
		assert.Empty(t, got, file)
	}
	got, _ = common(t, with(map[string]string{".github/workflows/ci.yml": workflow, ".github/actionlint.yml": "self-hosted-runner:\n  labels: [runs-on/fleet=kvm/env=production]\n"}))
	assert.Contains(t, got["common:label:runs-on/fleet=docker8/env=production"], ".github/actionlint.yml does not name")
	assert.Len(t, got, 1)
}

// RELEASE says how the repository is released, and the files hold it to what it says: a
// workflow that releases where it says none, a workflow or a job named that is not there.
func TestReleaseIsStatedAndTrue(t *testing.T) {
	release := func(m string) string {
		return taskfile("  common:\n    taskfile: \"{{.GO_TOOLS_TASKS}}\"\n    flatten: true\n    vars:\n      RELEASE:\n        map: "+m+"\n", lint)
	}
	const none = "{none: \"nothing is released\"}"
	gate := "on: push\njobs:\n  gate:\n    name: Gate\n    steps:\n      - run: go tool task lint\n  test:\n    name: Test ${{ matrix.os }}\n    steps:\n      - run: go tool task test\n  build:\n    steps:\n      - run: go tool task build\n"
	for name, tc := range map[string]struct {
		files map[string]string
		want  string // the finding's key, "" for none
		says  string // what the finding says, or the statement when there is none
	}{
		"none":                              {files: map[string]string{"Taskfile.yml": release(none)}, says: "RELEASE says none"},
		"no RELEASE":                        {files: map[string]string{"Taskfile.yml": taskfile("  common:\n    taskfile: \"{{.GO_TOOLS_TASKS}}\"\n    flatten: true\n", lint)}, want: "common:RELEASE", says: "Taskfile.yml:3: the include of go-tools' common tasks states no RELEASE"},
		"RELEASE a string":                  {files: map[string]string{"Taskfile.yml": taskfile("  common:\n    taskfile: \"{{.GO_TOOLS_TASKS}}\"\n    flatten: true\n    vars: {RELEASE: none}\n", lint)}, want: "common:RELEASE", says: "states no RELEASE"},
		"RELEASE with no map":               {files: map[string]string{"Taskfile.yml": release("x")}, want: "common:RELEASE", says: "states no RELEASE"},
		"RELEASE not under map":             {files: map[string]string{"Taskfile.yml": taskfile("  common:\n    taskfile: \"{{.GO_TOOLS_TASKS}}\"\n    flatten: true\n    vars:\n      RELEASE: {none: no}\n", lint)}, want: "common:RELEASE", says: "states no RELEASE"},
		"none, and an action that releases": {files: map[string]string{"Taskfile.yml": release(none), ".github/actions/up/action.yml": "runs:\n  using: composite\n  steps:\n    - run: gh release create v1\n      shell: bash\n"}, want: "common:RELEASE/none/action.yml", says: ".github/actions/up/action.yml: releases the repository (step \"#1\" runs `gh release create`)"},
		"RELEASE says nothing":              {files: map[string]string{"Taskfile.yml": release("{files: x.tar.gz}")}, want: "common:RELEASE", says: "RELEASE has neither `elsewhere`, `none`, nor both `proof` and `jobs`"},
		"proof without jobs":                {files: map[string]string{"Taskfile.yml": release("{proof: ci.yml}")}, want: "common:RELEASE", says: "RELEASE has neither"},
		"jobs without a proof":              {files: map[string]string{"Taskfile.yml": release("{jobs: Gate}")}, want: "common:RELEASE", says: "RELEASE has neither"},
		"proof and jobs":                    {files: map[string]string{"Taskfile.yml": release("{proof: ci.yml, jobs: \"Gate, Test (linux), build\"}"), ".github/workflows/ci.yml": gate}, says: "RELEASE says proof: ci.yml, jobs: Gate, Test (linux), build"},
		"a proof not there":                 {files: map[string]string{"Taskfile.yml": release("{proof: gate.yml, jobs: Gate}")}, want: "common:RELEASE/proof", says: "RELEASE's proof names gate.yml, and there is no .github/workflows/gate.yml"},
		"a job not there":                   {files: map[string]string{"Taskfile.yml": release("{proof: ci.yml, jobs: \"Gate,Lint\"}"), ".github/workflows/ci.yml": gate}, want: "common:RELEASE/jobs/Lint", says: "its jobs are \"Gate\", \"Test ${{ matrix.os }}\", \"build\""},
		"a job by its key":                  {files: map[string]string{"Taskfile.yml": release("{proof: ci.yml, jobs: gate}"), ".github/workflows/ci.yml": gate}, want: "common:RELEASE/jobs/gate", says: "a job is named by its `name:`, or its key when it has none"},
		"a matrix's other job":              {files: map[string]string{"Taskfile.yml": release("{proof: ci.yml, jobs: Tests}"), ".github/workflows/ci.yml": gate}, want: "common:RELEASE/jobs/Tests"},
		"elsewhere":                         {files: map[string]string{"Taskfile.yml": release("{elsewhere: release.yml}"), ".github/workflows/release.yml": "on: workflow_dispatch\n"}, says: "RELEASE says elsewhere: release.yml"},
		"elsewhere, not there":              {files: map[string]string{"Taskfile.yml": release("{elsewhere: release.yml metal.yml}"), ".github/workflows/release.yml": "on: workflow_dispatch\n"}, want: "common:RELEASE/elsewhere/metal.yml", says: "RELEASE's elsewhere names metal.yml, and there is no .github/workflows/metal.yml"},
		"elsewhere over none":               {files: map[string]string{"Taskfile.yml": release("{elsewhere: release.yml, none: no}"), ".github/workflows/release.yml": "on: workflow_dispatch\n"}, says: "RELEASE says elsewhere: release.yml"},
		"none, and release.yml":             {files: map[string]string{"Taskfile.yml": release(none), ".github/workflows/release.yml": "on: workflow_dispatch\n"}, want: "common:RELEASE/none/release.yml", says: ".github/workflows/release.yml: releases the repository (it is named release.yml), and RELEASE in Taskfile.yml says none: state `RELEASE: {map: {elsewhere: release.yml}}`"},
		"none, and a tag":                   {files: map[string]string{"Taskfile.yml": release(none), ".github/workflows/up.yml": "on:\n  push:\n    tags: ['v*']\n"}, want: "common:RELEASE/none/up.yml", says: "a tag pushed or a release published starts it"},
		"none, and a release":               {files: map[string]string{"Taskfile.yml": release(none), ".github/workflows/up.yml": "on:\n  release:\n    types: [published]\n"}, want: "common:RELEASE/none/up.yml", says: "a tag pushed or a release published starts it"},
		"none, and release":                 {files: map[string]string{"Taskfile.yml": release(none), ".github/workflows/up.yml": "on: release\n"}, want: "common:RELEASE/none/up.yml", says: "a tag pushed or a release published starts it"},
		"none, and [release]":               {files: map[string]string{"Taskfile.yml": release(none), ".github/workflows/up.yml": "on: [push, release]\n"}, want: "common:RELEASE/none/up.yml", says: "a tag pushed or a release published starts it"},
		"none, and a branch":                {files: map[string]string{"Taskfile.yml": release(none), ".github/workflows/up.yml": "on:\n  push:\n    branches: [main]\n"}, says: "RELEASE says none"},
		"none, and an action":               {files: map[string]string{"Taskfile.yml": release(none), ".github/workflows/up.yml": "on: push\njobs:\n  up:\n    steps:\n      - name: Up\n        uses: softprops/action-gh-release@0123 # v2\n"}, want: "common:RELEASE/none/up.yml", says: "step \"Up\" uses softprops/action-gh-release"},
		"none, and gh":                      {files: map[string]string{"Taskfile.yml": release(none), ".github/workflows/up.yml": "on: push\njobs:\n  up:\n    steps:\n      - run: gh release create v1 --notes x\n"}, want: "common:RELEASE/none/up.yml", says: "step \"#1\" runs `gh release create`"},
		"none, and a tag made":              {files: map[string]string{"Taskfile.yml": release(none), ".github/workflows/up.yml": "on: push\njobs:\n  up:\n    steps:\n      - run: git tag -a v1 -m v1\n"}, want: "common:RELEASE/none/up.yml", says: "runs `git tag -a`"},
		"none, and tags listed":             {files: map[string]string{"Taskfile.yml": release(none), ".github/workflows/up.yml": "on: push\njobs:\n  up:\n    steps:\n      - run: git tag -l\n"}, says: "RELEASE says none"},
		"none, and a task":                  {files: map[string]string{"Taskfile.yml": taskfile(strings.Replace(includeNone, "its outputs are copied", "no", 1), lint+"  up:\n    cmds:\n      - task: push\n  push:\n    cmds: [git push origin --tags]\n"), ".github/workflows/up.yml": "on: push\njobs:\n  up:\n    steps:\n      - run: go tool task up\n"}, want: "common:RELEASE/none/up.yml", says: "it runs task push, which runs `git push origin --tags` (Taskfile.yml:"},
		"none, and a task unrun":            {files: map[string]string{"Taskfile.yml": taskfile(includeNone, lint+"  push:\n    cmds: [git push origin --tags]\n")}, says: "RELEASE says none"},
	} {
		t.Run(name, func(t *testing.T) {
			got, said := common(t, with(tc.files))
			if tc.want == "" {
				assert.Empty(t, got)
				assert.Contains(t, said, tc.says)
				return
			}
			assert.Equal(t, []string{tc.want}, slices.Sorted(maps.Keys(got)))
			assert.Contains(t, got[tc.want], tc.says)
		})
	}
}

// A var a Taskfile of the repository states is what an included task reads under that name, over
// the include's: open-remote-ssh's root RELEASE, its publish directory, was the release task's
// RELEASE while the include stated `elsewhere`, and the task said no RELEASE was stated. Every
// name the common tasks read is one, and the rename after what it holds passes.
func TestARepositorysVarDoesNotHideOneTheCommonTasksRead(t *testing.T) {
	const include = "  common:\n    taskfile: \"{{.GO_TOOLS_TASKS}}\"\n    flatten: true\n    vars:\n      RELEASE:\n        map: {elsewhere: publish.yml}\n"
	openRemoteSSH := func(name string) map[string]string {
		return with(map[string]string{
			".github/workflows/publish.yml": "on:\n  push:\n    tags: ['v*']\n",
			"Taskfile.yml":                  "version: '3'\nvars: {" + name + ": build/release}\nincludes:\n" + include + "tasks:\n" + lint,
		})
	}
	got, said := common(t, openRemoteSSH("RELEASE"))
	assert.Equal(t, []string{"common:Taskfile.yml:vars:RELEASE"}, slices.Sorted(maps.Keys(got)))
	assert.Equal(t, "Taskfile.yml:2: var RELEASE is one the common tasks read, and a Taskfile's own var of that name is what they read instead of the include's or the command line's: "+
		"rename it after what it holds", got["common:Taskfile.yml:vars:RELEASE"])
	assert.Contains(t, said, "RELEASE says elsewhere: publish.yml", "the include's map is still what the rest of the gate reads")

	got, _ = common(t, openRemoteSSH("RELEASE_DIR"))
	assert.Empty(t, got)

	// Each name the common tasks read, in the root and in a file an include gives a namespace;
	// the release task's own vars, Task's, and a name they do not read are not.
	read := []string{"BACK", "CONSUMERS", "FILE", "GO_TOOLS", "NAME", "RELEASE", "RELEASE_DRY", "SHA", "TO", "VERSIONS_FILES", "WORKFLOWS_ALSO"}
	var vars strings.Builder
	for _, n := range append(slices.Clone(read), "RELEASE_STATED", "RELEASE_FILES", "CLI_ARGS", "TASK_EXE", "VERSION") {
		vars.WriteString("  " + n + ": x\n")
	}
	got, _ = common(t, with(map[string]string{
		"Taskfile.yml":     "version: '3'\nvars:\n" + vars.String() + "includes:\n" + includeNone + "  ci: {taskfile: taskfiles/ci.yml}\ntasks:\n" + lint,
		"taskfiles/ci.yml": "version: '3'\nvars:\n  SHA: '{{.GITHUB_SHA}}'\ntasks: {}\n",
	}))
	var want []string
	for _, n := range read {
		want = append(want, "common:Taskfile.yml:vars:"+n)
	}
	want = append(want, "common:taskfiles/ci.yml:vars:SHA")
	slices.Sort(want)
	assert.Equal(t, want, slices.Sorted(maps.Keys(got)))
	assert.Contains(t, got["common:taskfiles/ci.yml:vars:SHA"], "taskfiles/ci.yml:3: var SHA")
}

// RELEASE's keys are the ones the release task reads, each with a value. In a flow map an unquoted
// comma ends the value: runbooks' none sentence was cut there, the rest a key with no value, and
// the task and the gate both read the cut sentence and said nothing.
func TestReleasesKeysAreTheTasksEachWithAValue(t *testing.T) {
	runbooks := func(m string) map[string]string {
		return with(map[string]string{
			"Taskfile.yml":             taskfile("  common:\n    taskfile: \"{{.GO_TOOLS_TASKS}}\"\n    flatten: true\n    vars:\n      RELEASE:\n        map: "+m+"\n", lint),
			".github/workflows/ci.yml": "on: pull_request\njobs:\n  gate:\n    name: Gate\n    runs-on: ubuntu-latest\n    steps:\n      - run: go tool task lint\n",
		})
	}
	const sentence = "spin pins a commit of main and copies the runbooks in, so no tag or release names a version of them"
	got, _ := common(t, runbooks("{none: "+sentence+"}"))
	assert.Equal(t, []string{"common:RELEASE/key/so no tag or release names a version of them"}, slices.Sorted(maps.Keys(got)))
	assert.Equal(t, "Taskfile.yml:8: RELEASE's key \"so no tag or release names a version of them\" is not one of proof, jobs, files, elsewhere, none and has no value, "+
		"so it says nothing the release task reads. A value that holds a comma is quoted (`{none: \"a, b\"}`): unquoted, the comma ends it and the rest is a key of its own",
		got["common:RELEASE/key/so no tag or release names a version of them"])

	got, said := common(t, runbooks("{none: \""+sentence+"\"}"))
	assert.Empty(t, got)
	assert.Contains(t, said, "RELEASE says none")

	for m, says := range map[string]string{
		"{elswhere: release.yml}":                  "RELEASE's key \"elswhere\" is not one of proof, jobs, files, elsewhere, none, so",
		"{proof: ci.yml, job: Gate, jobs: Gate}":   "RELEASE's key \"job\" is not one of proof, jobs, files, elsewhere, none, so",
		"{none: \"nothing is released\", files: }": "RELEASE's key \"files\" has no value, so",
	} {
		got, _ := common(t, runbooks(m))
		key := "common:RELEASE/key/" + strings.Split(says, "\"")[1]
		require.Contains(t, got, key, m)
		assert.Contains(t, got[key], says, m)
	}
}
