---
title: The common tasks
description: versions, bump, release, lint:workflows and consumers, written once in go-tools and included by every repository's Taskfile - the same names, arguments and meaning in each, with no copy committed.
---

Every repository pins in `versions.yaml`, lints its workflows and - most of them - cuts releases.
Written per repository, the tasks for those drift: `bump` took `VERSION=` here and `TO=` there,
and a release was three tasks in one repository and a copied naming rule in another. So the tasks
are written once, in go-tools' `internal/tasks/tasks.yml`, and ship in its release beside the commands
they call.

## Take them

`go tool task` ([Getting started](../start/)) unpacks the release and names its `tasks.yml` in
`GO_TOOLS_TASKS`. A repository's Taskfile includes it:

```yaml title="Taskfile.yml"
includes:
  common:
    taskfile: '{{.GO_TOOLS_TASKS}}'
    flatten: true
    vars: {GATE_WORKFLOW: ci.yml, GATE_JOBS: Gate}
```

That is all a repository writes. `flatten` keeps the names as they are - `go tool task versions`,
not `common:versions` - and makes a second definition an error: a repository that writes its own
`versions` task is refused by Task for every task it is asked (`Found multiple tasks (versions)`),
so a name has one meaning everywhere. Nothing is fetched when a task runs: the file is in the
cache `go tool task` already keeps, at the release its commands are from.

It needs `go tool task`. A Task run directly has no `GO_TOOLS_TASKS`, and says `include cycle
detected`, since an empty path is the Taskfile itself.

## The tasks

| task | what it does |
|---|---|
| `versions` | says which pins are behind their upstream, in every file of pins; exits 1 when any is |
| `bump NAME=<entry> [TO=<version>] [FILE=<file>]` | pins one entry at a version, the newest by default |
| `release [SHA=<commit>]` | releases main's head as the next `v<YYYYMMDD>.<NN>`: asks [`proven`](../proven/) that the gate passed on the commit, names it with [`release`](../release/), and publishes it with `gh`, which tags it |
| `lint:workflows` | `actionlint` at the version `versions.yaml` pins (an `actionlint` entry of kind `module`), and a failure for an action used by a tag rather than a commit |
| `consumers` | the [`consumers`](../consumers/) report, for a repository with a `consumers.toml` |

## What a repository states

Only what differs, as the include's `vars`:

| var | meaning | when absent |
|---|---|---|
| `GATE_WORKFLOW`, `GATE_JOBS` | the workflow whose pass proves a commit, and the jobs that pass must have run (by the start of their name, comma-separated) | the repository does not release: `task release` is refused |
| `RELEASE_FILES` | files, or globs of them, a release carries | a release is its tag and its notes |
| `VERSIONS_FILES` | every file of pins the repository has | `versions.yaml` |
| `WORKFLOWS_ALSO` | a second `.github` directory, such as an example the repository publishes | `.github` alone |
| `CONSUMERS` | where the consumers table is | `consumers.toml` |

They are the include's vars and not the Taskfile's own because of how Task orders them: in an
included task a Taskfile's own var wins over the command line's. A repository with a root
`VERSION` would have it answer for `bump VERSION=...`, which is why the argument is `TO`.

A release is cut by a person: `go tool task release`, with `gh` logged in. go-tools' own
`release.yml` runs the same task once its binaries are built, with the run's `GH_TOKEN`.
