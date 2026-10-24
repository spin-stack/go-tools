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
      - run: make html
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

	findings, err := taskgate.Check(repo)
	require.NoError(t, err)
	type step struct {
		key  string
		line int
		run  string
	}
	var got []step
	for _, f := range findings {
		got = append(got, step{f.Key(), f.Line, f.Run})
	}
	assert.ElementsMatch(t, []step{
		{".github/workflows/ci.yml/gate/Shell", 17, "go test ./... ..."},
		{".github/workflows/ci.yml/gate/#8", 19, "task lint && echo done"},
		{".github/workflows/ci.yml/gate/#9", 20, "task mutate > out.txt"},
		{".github/workflows/ci.yml/gate/#10", 21, "go vet ./..."},
		{".github/workflows/ci.yml/docs/#1", 26, "make html"},
		{".github/workflows/release.yaml/publish/Push", 7, "docker push x"},
		{".github/actions/setup/action.yml/Go", 5, "go version"},
	}, got)
	assert.Contains(t, findings[0].String(), "one `task <name>`")
}
