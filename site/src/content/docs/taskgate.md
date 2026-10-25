---
title: taskgate
description: Fails on a workflow step that runs anything but one task, on a task that holds a program rather than a few commands, and on a script a task runs that decides anything - so what CI does is written once, and what decides is Go with a test.
---

A workflow that runs its own shell is a second copy of the build. It drifts from the Taskfile,
nobody can run it before pushing, and it is the one place where the shell was never reviewed by
anyone running it. `taskgate` holds every `run:` step of every workflow and composite action to
one command: `task <name>`.

That moves the shell into the Taskfile, and a Taskfile is a place to say which commands run and
in what order, not to write programs: a block inside `- |` gets no `set -euo pipefail` unless
somebody remembers it, mixes `{{.Var}}` templating with `$shell` expansion so quoting has two
layers, and reads as YAML rather than as code. So `taskgate` holds each task's shell to a few
lines, and the scripts a task calls to being scripts - a program that *decides* something from
what it reads has an answer a test could hold, and belongs in Go.

```sh
go tool taskgate [-allow file] [-max-lines n] [-max-script-lines n] [dir]
```

## What a step looks like

```yaml title=".github/workflows/ci.yml"
steps:
  - uses: actions/checkout@<commit> # v7.0.1
    with:
      fetch-depth: 0
  - uses: actions/setup-go@<commit> # v7.0.0
    with:
      go-version-file: go.mod
  - run: go tool task lint
  - run: go tool task test
  - name: What this change broke, and whether a test noticed
    env:
      BEFORE: ${{ github.event.before }}
    run: go tool task mutate
```

`uses:` steps set the machine up, `env:` hands the task what only the workflow knows, and the
`run:` is the task. Everything else - the shell, the conditions, what to write to
`$GITHUB_STEP_SUMMARY` - is in the task, where `task mutate` on a laptop runs the same lines.
A task reads `GITHUB_ACTIONS`, `GITHUB_EVENT_NAME` and the rest of a run's environment to do what
only CI does.

A step passes when its `run:` is a single `task ...` or `go tool task ...` line, with whatever
arguments and variables it is given. It fails on a second line, on `&&`, `||`, `;` or `|` after the
task, and on output redirected with `>`.

## What a task looks like

```yaml title="Taskfile.yml"
tasks:
  e2e:up:
    desc: The stack, on this machine.
    cmds:
      - docker compose up -d --wait
      - bash hack/wait-ready.sh http://localhost:8080/healthz
```

Every piece of shell in a Taskfile - each of a task's `cmds`, as a string or under `cmd:`, and
every `sh:` of a dynamic variable or a precondition - may hold `-max-lines` lines, 12 by default.
Lines are counted as a shell runs them: a comment, a blank line and a `\` continuation are not
lines. Every Taskfile git lists is read - the root's, a directory's own, and whatever one
`includes:` - so a block does not escape the limit by moving to another file.

A block over the limit is a program. What *drives the machine* - mounts, `mkfs`, `docker compose`,
`systemctl` - goes to a script the task calls, which has one shebang, one `set -euo pipefail` and a
reviewable diff. What *asserts* something goes to a test. What *decides* something goes to Go.

## What a script may be

A word in a task's shell that names a file in the repository, and that file is shell - named
`.sh`, or opening with a `#!` line that runs `sh`, `bash`, `dash` or `zsh` - is a script the task
runs, and `taskgate` reads it too. A script fails when it is longer than `-max-script-lines`
(40 by default, counted the same way), or when it decides: it writes to `$GITHUB_OUTPUT` or
`$GITHUB_ENV`, the answer the workflow reads next, or it reads JSON with `jq`. Each of those is a
function with a table test waiting to be written, and a shell script's only test is the run that
calls it - which is how a script that chose which commit CI tested against ran for two days on a
base nobody had asked for before anyone read it.

A script a script runs - a word of its shell naming a file in the repository, the same way - is
read too, under the same limits: a program cut into pieces behind a dispatcher is the same
program, and each piece answers for itself (the finding names the script that runs it).

A script that drives the machine and is under the limit is a script, and `taskgate` says nothing
about it.

## Exemptions

`-allow` names what is allowed on purpose, one a line, each with its reason:

```text title=".github/taskgate-allow.txt"
.github/workflows/ci.yml/gate/Task's pin  # read before Task is installed
Taskfile.yml:ci:lane  # the lane's environment, three lines over; owed to hack/ci-lanes.sh
hack/deadcode.sh  # 134 lines; owed to Go, with the allowlist it reads
```

A step is `<file>/<job>/<step>`, the step by its `name`, else its `id`, else `#n`, its place in the
job counting from 1; a composite action's has no job. A task's block is `<file>:<task>`, the
file's own `vars:` as `vars`. A script is its path. An exemption that matches nothing fails too,
so the list only shrinks - and a block or a script listed is work owed with its reason, not a
decision that it belongs where it is.

## Task from go.mod

`go get -tool github.com/go-task/task/v3/cmd/task` pins Task like any other tool, so CI needs only
`setup-go` and no step to install Task, and every developer has the same version with `go tool
task`. That is what an application repository should do.

A library is the exception: its tools' modules would be in the module graph of every repository
that imports it. go-tools itself installs Task with `go-task/setup-task`, from its pin in
`versions.yaml`, and the step that reads that pin is its one exemption.
