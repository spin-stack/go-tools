package taskgate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/internal/taskgate"
)

// A step is one task, run by Task or by the Task go.mod pins; anything else a workflow or a
// composite action runs is a finding, named by its file, job and step - the step's name, its id,
// or its place - and found at the line its shell starts on. A step that only uses an action runs
// no shell, and what is not a workflow is not read.
func TestEveryStepIsOneTask(t *testing.T) {
	repo := t.TempDir()
	write := func(rel, body string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(repo, rel)), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(repo, rel), []byte(body), 0o600))
	}
	write(".github/workflows/ci.yml", `on: push
jobs:
  gate:
    steps:
      - uses: actions/checkout@v6
      - run: task lint
      - name: Test
        run: go tool task test -- -run TestX
      - run: go tool task
      - run: task
      - id: summary
        run: |
          # what the run covered
          task coverage
      - name: Shell
        run: |
          go test ./...
          task lint
      - run: task lint && echo done
      - run: task mutate > out.txt
      - run: go vet ./...
  release:
    uses: ./.github/workflows/release.yaml
  docs:
    steps:
      - run: |
          # the pages
          make html
`)
	write(".github/workflows/release.yaml", "on: workflow_call\njobs:\n  publish:\n    steps:\n      - name: Push\n        run: >\n          docker push x\n")
	write(".github/workflows/empty.yml", "")
	write(".github/actions/setup/action.yml", "runs:\n  using: composite\n  steps:\n    - name: Go\n      run: go version\n      shell: bash\n    - run: task setup\n      shell: bash\n")
	// Shaped as a workflow and an action, and neither: GitHub runs only a workflows/ file and an
	// action's action.yml.
	write(".github/actions/setup/notes.yml", "runs:\n  steps:\n    - run: make\n")
	write(".github/actions/setup/action.md", "runs:\n  steps:\n    - run: make\n")
	write(".github/workflows/README.md", "jobs:\n  x:\n    steps:\n      - run: make\n")
	write(".github/workflows/old/ci.yml", "jobs:\n  x:\n    steps:\n      - run: make\n")
	write(".github/ci.yml", "jobs:\n  x:\n    steps:\n      - run: make\n")
	git := exec.Command("git", "init", "-q", repo)
	out, err := git.CombinedOutput()
	require.NoError(t, err, "%s", out)
	// Staged and then deleted: git lists it, and there is nothing to read.
	write(".github/workflows/staged.yml", "jobs:\n  x:\n    steps:\n      - run: make\n")
	out, err = exec.Command("git", "-C", repo, "add", ".github/workflows/staged.yml").CombinedOutput()
	require.NoError(t, err, "%s", out)
	require.NoError(t, os.Remove(filepath.Join(repo, ".github/workflows/staged.yml")))

	findings, err := taskgate.Check(repo, taskgate.Limits{}, nil)
	require.NoError(t, err)
	type step struct {
		key  string
		line int
		run  string
	}
	var got []step
	for _, f := range findings {
		s, ok := f.(taskgate.Step)
		require.True(t, ok, "%v", f)
		got = append(got, step{s.Key(), s.Line, s.Run})
	}
	assert.ElementsMatch(t, []step{
		{".github/workflows/ci.yml/gate/Shell", 17, "go test ./... ..."},
		{".github/workflows/ci.yml/gate/#8", 19, "task lint && echo done"},
		{".github/workflows/ci.yml/gate/#9", 20, "task mutate > out.txt"},
		{".github/workflows/ci.yml/gate/#10", 21, "go vet ./..."},
		{".github/workflows/ci.yml/docs/#1", 27, "make html"},
		{".github/workflows/release.yaml/publish/Push", 7, "docker push x"},
		{".github/actions/setup/action.yml/Go", 5, "go version"},
	}, got)
	assert.Contains(t, findings[0].String(), "one `task <name>`")
}

// A step that is one task names tasks the Taskfile must have: each word after task that is not a
// flag, a VAR=value or what follows `--`. A name the Taskfile lacks is Missing, keyed by its step
// and the name; a ${{ }} only a run fills in is not looked up; and nil asks nothing of the names.
func TestAStepRunsATaskTheTaskfileHas(t *testing.T) {
	root := repo(t, map[string]string{
		".github/workflows/ci.yml": `on: push
jobs:
  gate:
    steps:
      - run: task lint
      - name: Test
        run: go tool task test -v SHARD=1 TITLE="a (b) c" -- gone
      - run: task build gone:too "$SHARD"
      - run: task ${{ matrix.task }} VERSION=1
      - run: task l
`,
		".github/actions/setup/action.yml": "runs:\n  using: composite\n  steps:\n    - name: Setup\n      run: task setup\n      shell: bash\n",
	})
	known := map[string]bool{"lint": true, "test": true, "build": true, "l": true}

	findings, err := taskgate.Check(root, taskgate.Limits{}, known)
	require.NoError(t, err)
	var got []string
	for _, f := range findings {
		m, ok := f.(taskgate.Missing)
		require.True(t, ok, "%v", f)
		got = append(got, m.Key())
	}
	assert.ElementsMatch(t, []string{
		".github/workflows/ci.yml/gate/#3/gone:too",
		".github/actions/setup/action.yml/Setup/setup",
	}, got)
	require.NotEmpty(t, findings)
	assert.Contains(t, findings[0].String(), "has no task")

	none, err := taskgate.Check(root, taskgate.Limits{}, nil)
	require.NoError(t, err)
	assert.Empty(t, none)
}

// The names are Task's own answer: each task's name and its aliases as the binary given lists them
// for the directory, asked not to write the checksums a listing otherwise leaves. A Task that
// fails, or lists nothing, is an error rather than every step Missing.
func TestTheNamesAreWhatTaskLists(t *testing.T) {
	bin := t.TempDir()
	stub := func(name, body string) string {
		p := filepath.Join(bin, name)
		require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o700)) //nolint:gosec // a stub run by the test
		return p
	}
	args := filepath.Join(bin, "args")
	lists := stub("task", `echo "$@" > `+args+`
echo '{"tasks":[{"name":"lint","aliases":["l"]},{"name":"e2e:up","aliases":[]}]}'
`)
	got, err := taskgate.Tasks(lists, "/repo")
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"lint": true, "l": true, "e2e:up": true}, got)
	said, err := os.ReadFile(args) //nolint:gosec // the stub's own file
	require.NoError(t, err)
	assert.Equal(t, "--dir /repo --list-all --json --no-status\n", string(said))

	for name, body := range map[string]string{
		"fails": "echo 'no Taskfile found' >&2; exit 200\n",
		"empty": `echo '{"tasks":[]}'` + "\n",
		"prose": "echo hello\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := taskgate.Tasks(stub(name, body), "/repo")
			require.Error(t, err)
			if name == "fails" {
				assert.Contains(t, err.Error(), "no Taskfile found")
			}
		})
	}
}
