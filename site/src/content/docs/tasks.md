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

## Which release the tasks are from

Two versions of go-tools are at work in a repository, and they are not the same one:

- `go.mod` requires a module version, `v0.<YYYYMMDD>.<N>`. It is what `go tool task` itself is
  compiled from, and what the repository's tests link: the [versions gate](../versions/#the-gate),
  the `consumers` package.
- The tasks, and every command they call, are go-tools' **latest release**, whatever `go.mod`
  says. A release changes what `versions`, `bump`, `release`, `lint:workflows` and `consumers` do
  in a repository at its next run, with no commit there. That is the point - one meaning
  everywhere, the day it changes - and it means a red `lint:workflows` on an untouched branch can
  be a release of go-tools: [its releases](https://github.com/spin-stack/go-tools/releases) say
  what changed.

The latest release is never one from before `go.mod`'s. Which release is the latest is asked at
most every ten minutes, so a machine can go on naming the one before for that long; but
`go tool task` knows the release it was compiled from, and asks again when the cache names an
earlier one. So a repository that takes something a release added - these tasks, a new command, a
new var - requires that release in the same commit, and no machine runs the new Taskfile beside
the old release:

```sh
go get github.com/spin-stack/go-tools@latest && go mod tidy
```

`go get` also moves the modules go-tools requires that the repository requires too (taking the
tasks moved brand's `github.com/BurntSushi/toml` to v1.6.0), and `go mod tidy` drops the ones the
repository no longer imports. Both belong in the commit.

The other way round needs nothing: a repository whose `go.mod` is behind runs the latest tasks
with the `go tool task` it has. What it does not have is what a newer gate knows. The gate counts
the pins the common tasks read as read, from the tasks of the version `go.mod` requires; when a
release makes a task read a new pin, the task says the entry is missing at its next run, and the
gate says it once `go.mod` has moved.

## The tasks

| task | what it does |
|---|---|
| `versions` | says which pins are behind their upstream, in every file of pins; exits 1 when any is |
| `bump NAME=<entry> [TO=<version>] [FILE=<file>]` | pins one entry at a version, the newest by default |
| `release [SHA=<commit>] [RELEASE_DRY=1]` | releases main's head as the next `v<YYYYMMDD>.<NN>`: asks [`proven`](../proven/) that the gate passed on the commit, names it with [`release`](../release/), and publishes it with `gh`, which tags it |
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

`bump VERSION=<version>`, the spelling three repositories had, is not refused: it pins the newest,
as `bump` with no version does. The task cannot refuse it. Under Task 3.54.0 an included task
reads `VERSION` the same whether the command line set it or the repository's Taskfile did - in a
repository with a root `VERSION`, `task bump` and `task bump VERSION=v9` both read the root's
value - so a refusal would refuse every bump in spin. Read the line `bump` prints: it names the
version it pinned.

## Releasing

A release is cut by a person: `go tool task release`, with `gh` logged in. go-tools' own
`release.yml` runs the same task once its binaries are built, with the run's `GH_TOKEN`.

`go tool task release RELEASE_DRY=1` is the release without the release. It fetches, asks
`proven`, names the commit and lists `RELEASE_FILES`, so it refuses what a release would - a
commit the gate has not passed on, one released already, a file that is not there - and then
prints the name and stops before `gh release create`:

```
RELEASE_DRY: 0123abc would be released to spin-stack/brand as v20261122.01, and is not
```

It is how a repository that has just taken the tasks proves its `GATE_WORKFLOW` and `GATE_JOBS`
on a main that is proven and not yet released.

## Every task is listed everywhere

`go tool task --list` shows all five in every repository that includes them, whether or not they
apply there. The ones that do not apply refuse, before running anything:

| task | where | it says | exit |
|---|---|---|---|
| `release` | the include sets no `GATE_WORKFLOW` and `GATE_JOBS`: the repository does not release | `Task "release" cancelled because it is missing required variables: GATE_WORKFLOW, GATE_JOBS` | 206 |
| `consumers` | there is no `consumers.toml` | `no consumers.toml: this repository names nobody that copies from it` | 201 |
| `bump` | no `NAME` was given | `Task "bump" cancelled because it is missing required variables: NAME` | 206 |

## What a failure means

| it says | what happened | what to do |
|---|---|---|
| `include cycle detected between <Taskfile> <--> <Taskfile>` (exit 110), for every task | `GO_TOOLS_TASKS` is empty: Task was run directly, or `go.mod` requires a go-tools from before `v0.20261122.4`, whose `go tool task` does not set it | run `go tool task`; `go get github.com/spin-stack/go-tools@latest` |
| `Found multiple tasks (versions) included by "common"` (exit 203), for every task | the repository's Taskfile defines a common task itself | delete the repository's own: the name has one meaning |
| `No Taskfile found at ".../tasks.yml"` (exit 100), for every task, after a line from `go tool task` that starts `the go-tools release this machine has ... has no tasks.yml` | the release in the cache is from before the tasks shipped, and no newer one could be had: GitHub could not be reached, or `go tool task` was built from a commit that is no release's and the cache named the latest less than ten minutes ago | run it again when `https://github.com/spin-stack/go-tools/releases/latest` can be reached, or ten minutes later |
| `versions: there is no actionlint in versions.yaml`, from `lint:workflows` | the task runs `actionlint` at the repository's pin, and there is none | add the `actionlint` entry (kind `module`, source `github.com/rhysd/actionlint/cmd/actionlint`) |
| `Taskfile.yml includes go-tools' tasks, and its task lint:workflows reads actionlint: versions: there is no actionlint in versions.yaml`, from the repository's tests | the same, found by the [versions gate](../versions/#the-gate) before the task is run | the same |
| `versions.yaml pins actionlint, and nothing reads it`, from the repository's tests, in a repository that includes the tasks | `go.mod` requires a go-tools whose gate does not count what the common tasks read (`v0.20261122.4` or before), or the include is not one the gate reads: it is a Taskfile's `includes` entry whose `taskfile` is `'{{.GO_TOOLS_TASKS}}'` | `go get github.com/spin-stack/go-tools@latest`; never a comment that spells the read |
| `missing required variables: GATE_WORKFLOW, GATE_JOBS` (exit 206), `no consumers.toml` (exit 201) | a task that does not apply to this repository | nothing: see above |
| `consumers.toml: scope is no field of the table`, from `consumers` | the table has `scope = "..."`; it is `scopes`, a list | `scopes = ["@fontsource", "@fontsource-variable"]` |
