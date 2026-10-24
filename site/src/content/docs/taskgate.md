---
title: taskgate
description: Fails on a workflow step that runs anything but one task, so what CI does is written once, in the Taskfile, and a developer runs exactly the same thing.
---

A workflow that runs its own shell is a second copy of the build. It drifts from the Taskfile,
nobody can run it before pushing, and it is the one place where the shell was never reviewed by
anyone running it. `taskgate` holds every `run:` step of every workflow and composite action to
one command: `task <name>`.

```sh
go tool taskgate [-allow file] [dir]
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

## Task from go.mod

`go get -tool github.com/go-task/task/v3/cmd/task` pins Task like any other tool, so CI needs only
`setup-go` and no step to install Task, and every developer has the same version with `go tool
task`. That is what an application repository should do.

A library is the exception: its tools' modules would be in the module graph of every repository
that imports it. go-tools itself installs Task with `go-task/setup-task`, from its pin in
`versions.yaml`, and the step that reads that pin is its one exemption.

## Exemptions

`-allow` names the steps that are not a task on purpose, one a line, each with its reason:

```text title=".github/taskgate-allow.txt"
.github/workflows/ci.yml/gate/Task's pin  # read before Task is installed
```

A step is `<file>/<job>/<step>`, the step by its `name`, else its `id`, else `#n`, its place in the
job counting from 1. A composite action's has no job. An exemption that matches no step fails
too, so the list only shrinks.
