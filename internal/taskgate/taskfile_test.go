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

// repo is a git repository under t.TempDir() holding files, each by its path.
func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte(body), 0o600))
	}
	out, err := exec.Command("git", "init", "-q", root).CombinedOutput()
	require.NoError(t, err, "%s", out)
	return root
}

// A task is a few lines that say which commands run: a cmd, a `cmd:`, a dynamic var's `sh:` or a
// precondition's over the limit is a block, named by its task - the file's own vars as vars - and
// counted as a shell runs it, comments and blank lines left out. One at the limit passes, and a
// limit of zero holds nothing.
func TestATaskHoldsCommandsNotAProgram(t *testing.T) {
	root := repo(t, map[string]string{
		"Taskfile.yml": `version: '3'
vars:
  SHORT:
    sh: echo one
  LONG:
    sh: |
      a
      b
      c
tasks:
  fine:
    cmds:
      - echo one
      - |
        # two lines of shell, as many as the limit

        echo two
        echo three
  over:
    desc: |
      three lines
      of prose
      are not shell
    sources:
      - |
        a
        b
        c
    cmds:
      - task: fine
      - cmd: |
          echo one
          echo two
          echo three
  odd:
    cmds:
      - cmd: {a: b}
      - sh: [a, b, c]
      - cmds: |
          a
          b
          c
  guarded:
    preconditions:
      - sh: |
          test -f a &&
            test -f b
          test -f c
        msg: files
    cmds:
      - echo ok
  looped:
    cmds:
      - for: [a, b]
        cmd: |
          echo {{.ITEM}} one
          echo {{.ITEM}} two
          echo {{.ITEM}} three
`,
	})
	findings, err := taskgate.Check(root, taskgate.Limits{Block: 2})
	require.NoError(t, err)
	var got []taskgate.Block
	for _, f := range findings {
		b, ok := f.(taskgate.Block)
		require.True(t, ok, "%v", f)
		got = append(got, b)
	}
	assert.ElementsMatch(t, []taskgate.Block{
		{File: "Taskfile.yml", Task: "vars", Line: 7, Lines: 3, Limit: 2},
		{File: "Taskfile.yml", Task: "over", Line: 32, Lines: 3, Limit: 2},
		{File: "Taskfile.yml", Task: "guarded", Line: 46, Lines: 3, Limit: 2},
		{File: "Taskfile.yml", Task: "looped", Line: 56, Lines: 3, Limit: 2},
	}, got)
	assert.Equal(t, "Taskfile.yml:over", got[1].Key())
	assert.Equal(t, "Taskfile.yml:32: task over holds 3 lines of shell (limit 2): a task says which commands run, in what order; a program goes in a script it calls, or in Go when it decides anything", got[1].String())

	findings, err = taskgate.Check(root, taskgate.Limits{})
	require.NoError(t, err)
	assert.Empty(t, findings)
}

// Every Taskfile Task reads is held: the root's by any of its names, a directory's own, and what
// one includes - as a path, under taskfile:, or as a directory - when git lists it. An empty one
// holds nothing.
func TestEveryTaskfileIsRead(t *testing.T) {
	long := "cmds:\n      - |\n        a\n        b\n"
	root := repo(t, map[string]string{
		"Taskfile.yaml":            "includes:\n  d: ./deploy\n  s: taskfiles/storage.yml\n  m:\n    taskfile: ./more/Taskfile.dist.yml\n  gone: ./nowhere.yml\n  dir: {dir: ./deploy}\n  none: ''\n  b: taskfiles/build.yml\ntasks:\n  root:\n    " + long,
		"taskfiles/build.yml":      "tasks:\n  image:\n    " + long,
		"onlyvars/Taskfile.yml":    "vars:\n  X:\n    sh: |\n      a\n      b\n",
		"taskfiles/storage.yml":    "tasks:\n  bucket:\n    " + long,
		"deploy/Taskfile.yml":      "tasks:\n  up:\n    " + long,
		"more/Taskfile.dist.yml":   "tasks:\n  more:\n    " + long,
		"site/Taskfile.yml":        "tasks:\n  build:\n    " + long,
		"notes/Taskfile.yml":       "",
		"vars/Taskfile.yml":        "vars:\n  X:\n    sh: |\n      a\n      b\ntasks: [odd]\n",
		"taskfiles/unincluded.yml": "tasks:\n  loose:\n    " + long,
		"docs/taskfile.md":         "tasks:\n  prose:\n    " + long,
	})
	findings, err := taskgate.Check(root, taskgate.Limits{Block: 1})
	require.NoError(t, err)
	var keys []string
	for _, f := range findings {
		keys = append(keys, f.Key())
	}
	assert.ElementsMatch(t, []string{
		"Taskfile.yaml:root", "taskfiles/storage.yml:bucket", "deploy/Taskfile.yml:up",
		"more/Taskfile.dist.yml:more", "site/Taskfile.yml:build", "vars/Taskfile.yml:vars",
		"taskfiles/build.yml:image", "onlyvars/Taskfile.yml:vars",
	}, keys)
}

// A script a task runs - a word that names a file git lists, beside the Taskfile or at the root,
// however it is quoted, which is shell by its .sh name or its #! line - is a program when it is
// longer than the limit or when it decides: it writes what the workflow reads next, or reads JSON
// with jq. One that is neither is a script; a file a task names that is not shell - a Python
// script, a Go file, a config, go.mod - is not read for either. The finding is the script's, named
// by its path, whichever tasks run it, and says the first.
func TestAScriptATaskRunsIsNotAProgram(t *testing.T) {
	root := repo(t, map[string]string{
		"Taskfile.yml": `tasks:
  base:
    cmds:
      - bash hack/ci-base.sh
  lanes:
    cmds:
      - echo "lanes=$(bash hack/ci-base.sh --list)"
      - "hack/long.sh"
      - ./hack/short.sh && (hack/jq.sh)
      - bash hack/missing.sh
      - bash hack/staged.sh
      - bash hack/quickwit --version 2>/dev/null
      - python3 hack/tidy
      - go run ./hack/lanes/cmd/lanes touched go.mod configs/a.yaml
  sub:
    dir: sub
    cmds:
      - task: base
`,
		"sub/Taskfile.yml":             "tasks:\n  local:\n    cmds:\n      - ./run.sh\n      - bash hack/short.sh\n",
		"sub/run.sh":                   "#!/bin/sh\necho 'name=x' >> \"${GITHUB_ENV}\"\n",
		"hack/ci-base.sh":              "#!/usr/bin/env bash\n# decides\nset -euo pipefail\necho base=1 >> \"${GITHUB_OUTPUT:-/dev/stdout}\"\ngh api x --jq .y\nxjq --notjq\necho proven=1 >> \"${GITHUB_OUTPUT}\"\n",
		"hack/staged.sh":               "#!/bin/sh\na\nb\nc\n",
		"hack/quickwit":                "#!/usr/bin/env bash\nset -euo pipefail\necho 'version=1' >> \"${GITHUB_OUTPUT}\"\n",
		"hack/tidy":                    "#!/usr/bin/env python3\nimport os\nprint(os.environ['GITHUB_OUTPUT'])\n",
		"hack/lanes/cmd/lanes/main.go": "package main\n// GITHUB_OUTPUT is written by jq here, says a comment\n",
		"go.mod":                       "module x\n",
		"configs/a.yaml":               "a: GITHUB_OUTPUT\n",
		"hack/long.sh":                 "#!/bin/sh\n# three\n\na\nb\nc\n",
		"hack/short.sh":                "#!/bin/sh\n# one\na\nb\n",
		"hack/jq.sh":                   "#!/bin/sh\ncurl x | jq .a\n",
	})
	// Staged and then deleted: git lists it, and there is nothing to read.
	out, err := exec.Command("git", "-C", root, "add", "hack/staged.sh").CombinedOutput()
	require.NoError(t, err, "%s", out)
	require.NoError(t, os.Remove(filepath.Join(root, "hack/staged.sh")))
	findings, err := taskgate.Check(root, taskgate.Limits{Script: 2})
	require.NoError(t, err)
	var got []taskgate.Script
	for _, f := range findings {
		s, ok := f.(taskgate.Script)
		require.True(t, ok, "%v", f)
		got = append(got, s)
	}
	assert.ElementsMatch(t, []taskgate.Script{
		{File: "hack/ci-base.sh", By: "Taskfile.yml:base", Lines: 5, Limit: 2, Reads: []string{"GITHUB_OUTPUT", "jq"}},
		{File: "hack/long.sh", By: "Taskfile.yml:lanes", Lines: 3, Limit: 2},
		{File: "hack/jq.sh", By: "Taskfile.yml:lanes", Lines: 1, Limit: 2, Reads: []string{"jq"}},
		{File: "sub/run.sh", By: "sub/Taskfile.yml:local", Lines: 1, Limit: 2, Reads: []string{"GITHUB_ENV"}},
		{File: "hack/quickwit", By: "Taskfile.yml:lanes", Lines: 2, Limit: 2, Reads: []string{"GITHUB_OUTPUT"}},
	}, got)
	for _, s := range got {
		if s.File == "hack/ci-base.sh" {
			assert.Equal(t, "hack/ci-base.sh", s.Key())
			assert.Equal(t, "hack/ci-base.sh:1: 5 lines of shell (limit 2) and decides by GITHUB_OUTPUT, jq: a program, whose only test is the run that calls it (Taskfile.yml:base); Go, with a test", s.String())
		}
		if s.File == "hack/jq.sh" {
			assert.Equal(t, "hack/jq.sh:1: decides by jq: a program, whose only test is the run that calls it (Taskfile.yml:lanes); Go, with a test", s.String())
		}
		if s.File == "hack/long.sh" {
			assert.Equal(t, "hack/long.sh:1: 3 lines of shell (limit 2): a program, whose only test is the run that calls it (Taskfile.yml:lanes); Go, with a test", s.String())
		}
	}

	// The limit is a count: one line over it is over.
	findings, err = taskgate.Check(root, taskgate.Limits{Script: 1})
	require.NoError(t, err)
	var files []string
	for _, f := range findings {
		files = append(files, f.Key())
	}
	assert.ElementsMatch(t, []string{"hack/ci-base.sh", "hack/long.sh", "hack/short.sh", "hack/jq.sh", "sub/run.sh", "hack/quickwit"}, files)
	assert.Equal(t, "hack/x.sh:1: 2 lines of shell (limit 1): a program, whose only test is the run that calls it (Taskfile.yml:x); Go, with a test",
		taskgate.Script{File: "hack/x.sh", By: "Taskfile.yml:x", Lines: 2, Limit: 1}.String())
}
