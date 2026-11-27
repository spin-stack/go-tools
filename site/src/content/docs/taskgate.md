---
title: taskgate
description: Fails on a workflow step that runs anything but one task or names one the Taskfile does not have, on a task that holds a program rather than a few commands, and on a script a task runs that decides anything - so what CI does is written once, and what decides is Go with a test.
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
go tool taskgate [-common] [-allow file] [-task path] [-command cmd]... [-max-lines n] [-max-script-lines n] [dir]
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

## Or the repository's own command

A repository whose interface is a command of its own - spin-ami's `spin-os build`, `spin-os
upgrade` - names it with `-command`, one flag for each way a step spells it:

```sh
go tool taskgate -command "go run ./cmd/spin-os" -command spin-os
```

A step that is one invocation of it passes as a task does: the same command a developer types,
whose logic is Go with tests. The rule is the task's: one command, with nothing around it - a `|`,
`&&` or `>` after it is a finding. The verb it names is not looked up: the command refuses one it
does not have, the first time the step runs.

## The task is one the Taskfile has

A step that is one task must name a task the Taskfile has. actionlint resolves a job's `needs:`
and knows nothing of what its `run:` does, so a task renamed or deleted is otherwise a red run the
next time the job runs - forty seconds into a job that was meant to prove something else, and on
a schedule, perhaps days later.

The names are Task's own answer: `taskgate` runs `task --list-all --json` in the directory and
looks each name up among the tasks and their aliases, so includes, namespaces and aliases resolve
as Task resolves them, and an `internal: true` task - which a workflow may not call - is missing.
`-task` is the binary that answers; give it the one the Taskfile runs under, so the version that
resolves a name is the one the workflow runs:

```yaml title="Taskfile.yml"
lint:workflows:
  cmds:
    - go tool taskgate -allow .github/taskgate-allow.txt -task {{.TASK_EXE}}
```

Every word after `task` is a name, up to a `--`, except a flag, a `VAR=value`, and what only a run
knows: a `${{ }}` expression or a shell `$VARIABLE`. Words are split as a shell splits them, so
`TITLE="unit (hermetic)"` is one variable.

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

## The common tasks (-common)

`-common` also holds the repository to [the common tasks](../tasks/) as every repository has
them. It is asked of a repository whose `go.mod` takes `go tool task`, that has a
`versions.yaml`, or whose Taskfile includes the tasks; of any other it asks nothing and says so.
It reads the files and runs nothing, Task least of all: a repository with a second task of a
common name is one Task refuses to load, and the finding is the one that names the file.

| rule | the finding's key |
|---|---|
| `go.mod` requires a go-tools from `v0.20261122.4` or later, the first whose `go tool task` sets `GO_TOOLS_TASKS` | `common:go-tools` |
| the root Taskfile includes `{{.GO_TOOLS_TASKS}}`, with `flatten: true`, so the names are `versions` and not `common:versions` | `common:include` |
| the include has no `excludes` | `common:excludes` |
| no Taskfile of the repository's defines a common name: in the root, or in a file an include gives a namespace (`workflows` in a file included as `lint` is `lint:workflows`) | `common:<file>:<task>` |
| no Taskfile of the repository's states, in its own `vars:`, a var the common tasks read (`RELEASE`, `VERSIONS_FILES`, `WORKFLOWS_ALSO`, `CONSUMERS`, `GO_TOOLS`, `TO`, `NAME`, `FILE`, `BACK`, `SHA`, `RELEASE_DRY`): an included task reads that one, not the include's, so a root `RELEASE` that is a directory leaves `release` saying the include states none. Rename it after what it holds | `common:<file>:vars:<var>` |
| in a repository that takes `go tool task`, no step runs a bare `task` or installs Task with `go-task/setup-task` | `common:<file>/<job>/<step>/task`, `.../setup-task` |
| `RELEASE` is stated: `{proof, jobs}`, `{elsewhere}` or `{none}` | `common:RELEASE` |
| `proof` is a workflow the repository has, and each of `jobs` starts the name of one of its jobs | `common:RELEASE/proof`, `common:RELEASE/jobs/<job>` |
| each file `elsewhere` names is under `.github/workflows` | `common:RELEASE/elsewhere/<file>` |
| with `none`, no workflow releases: none is named `release.yml`, started by a tag or a published release, uses a release action, or runs - in a step or in a task a step reaches - `gh release create`, `git tag <name>`, a push of tags or `goreleaser release` | `common:RELEASE/none/<file>` |
| `lint:workflows` is reached from a task a workflow runs, through `task:`, `deps` and Task run from a task's shell | `common:lint:workflows` |
| every runner label is GitHub's, or `.github/actionlint.yaml` names it | `common:label:<label>` |

The `none` rule reads what the files say. A release made by a program a step runs - a command of
the repository's own, a script - is not read, so a statement of `none` is checked against the
evidence there is, not proven. The reach of `lint:workflows` is the same: a task whose name a
template or a program makes is not followed, and a repository that runs its lint that way exempts
the finding with why.

```yaml title="Taskfile.yml"
lint:
  cmds:
    - go tool taskgate -common -allow .github/taskgate-allow.txt -task {{.TASK_EXE}}
    - task: lint:workflows
```

`-common-include` is the taskfile an include names to have the tasks, `{{.GO_TOOLS_TASKS}}` by
default. go-tools includes its tree's file and passes `-common-include internal/tasks/tasks.yml`.

The name of the task CI runs is not one of the rules. It is `lint`, `check` or `gate`, whichever
the repository has, and the gate asks only that it reaches `lint:workflows`.

## Exemptions

`-allow` names what is allowed on purpose, one a line, each with its reason:

```text title=".github/taskgate-allow.txt"
.github/workflows/ci.yml/gate/Task's pin  # read before Task is installed
Taskfile.yml:ci:lane  # the lane's environment, three lines over; owed to hack/ci-lanes.sh
hack/deadcode.sh  # 134 lines; owed to Go, with the allowlist it reads
```

A step is `<file>/<job>/<step>`, the step by its `name`, else its `id`, else `#n`, its place in the
job counting from 1; a composite action's has no job. A task a step names that the Taskfile has
not is the step's key and the name, `<file>/<job>/<step>/<task>`. A task's block is `<file>:<task>`, the
file's own `vars:` as `vars`. A script is its path. A finding of `-common` prints its key after it, in parentheses. An exemption that matches nothing fails too,
so the list only shrinks - and a block or a script listed is work owed with its reason, not a
decision that it belongs where it is.

## Task from go.mod

`go get -tool github.com/go-task/task/v3/cmd/task` pins Task like any other tool, so CI needs only
`setup-go` and no step to install Task, and every developer has the same version with `go tool
task`. That is what an application repository should do.

A library is the exception: its tools' modules would be in the module graph of every repository
that imports it. go-tools itself installs Task with `go-task/setup-task`, from its pin in
`versions.yaml`, and the step that reads that pin is its one exemption.
