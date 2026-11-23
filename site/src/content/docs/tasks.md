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
```

That is all a repository writes, unless [this task cuts its releases](#releasing). `flatten` keeps the names as they are - `go tool task versions`,
not `common:versions` - and makes a second definition an error: a repository that writes its own
`versions` task is refused by Task for every task it is asked (`Found multiple tasks (versions)`),
so a name has one meaning everywhere. Nothing is fetched when a task runs: the file is in the
cache `go tool task` already keeps, at the release its commands are from.

It needs `go tool task`. A Task run directly has no `GO_TOOLS_TASKS`, and says `include cycle
detected`, since an empty path is the Taskfile itself.

The quotes are YAML's and either kind will do: `"{{.GO_TOOLS_TASKS}}"` is the same include to
Task and to the [versions gate](../versions/#the-gate), which reads the value and not how it is
written. A repository that formats its YAML with Biome writes double quotes: the blog and
website each failed their own lint on the single-quoted line.

### A task of the repository's with one of these names

A repository whose Taskfile already has a `release`, a `versions` or a `bump` that means something
else renames its own, after what the task does: devcontainer-cli's `release` ran GoReleaser and is
`release:artifacts` now. `excludes: [release]` on the include is not a way out. Task accepts it,
and `go tool task release` would then run GoReleaser in that repository and cut a release in every
other, when the one thing the include is for is that a name means the same everywhere.

### Put `lint:workflows` in what CI runs

Nothing runs a common task unless the repository's own tasks do. `lint:workflows` belongs in the
task the workflow runs - `lint`, `check` or `gate`, whichever the repository has - and
[`taskgate -common`](../taskgate/#the-common-tasks--common) fails a repository where none does:

```yaml title="Taskfile.yml"
tasks:
  lint:
    cmds:
      - task: lint:workflows
```

It needs two things in the repository. An `actionlint` entry in `versions.yaml`, at the version
the other repositories pin, which is go-tools' own (`versions.yaml` at its root):

```yaml title="versions.yaml"
- name: actionlint
  kind: module
  source: github.com/rhysd/actionlint/cmd/actionlint
  version: v1.7.12
  track: github-release https://github.com/rhysd/actionlint
  note: task lint:workflows.
```

And, in a repository whose jobs run on spin-stack's own runners, the labels those runners have:
actionlint knows GitHub's, and fails the first run on any other with
`label "runs-on/fleet=docker8/env=production" is unknown`.

```yaml title=".github/actionlint.yaml"
self-hosted-runner:
  labels:
    # spin-stack/ci's fleets this repository's workflows run on.
    - runs-on/fleet=kvm/env=production
    - runs-on/fleet=docker8/env=production
```

On a pass the task prints nothing but the commands it ran. Its first run in a repository often
finds something real: an action used by a tag, or by a commit with no `# vX.Y.Z` after it.

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

Two rules follow, and both were learned by breaking brand
([#135](https://github.com/spin-stack/go-tools/issues/135)):

- **A file read by both a task and a test moves with `go.mod`, in one commit.** The task reads
  `consumers.toml` or `versions.yaml` with the latest release; the repository's test reads the same
  file with the module `go.mod` requires. When a release changes what the file may say - a field
  renamed, a field added - the new spelling fails the test until `go.mod` moves, and the old one
  fails the task from the day of the release. There is no order of two commits in which both pass.
- **After a go-tools release, a task that is not in the repository's CI can be broken with no
  commit, and nothing is red.** CI proves the tasks its own task calls and no others. brand's
  `consumers` failed on a branch that showed green, because the release had renamed a field of its
  table and `consumers` is in no workflow. After a release that says it changed a task, and
  before a bump of `go.mod`, run each common task the repository uses on an untouched branch:
  `go tool task --list`, `versions`, `consumers`, and `release RELEASE_DRY=1` where this task
  releases the repository.

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
| `versions` | says which pins are behind their upstream, in every file of pins, with `gh`'s token when none is set. It fails when any is behind and when [an entry could not be read](../versions/#an-entry-check-could-not-read): through Task both are exit 201, and the last line of each file's rows says which (`N behind; go tool task bump NAME=<name> moves one`, or `N not read, and ...`) |
| `bump NAME=<entry> [TO=<version>] [FILE=<file>] [BACK=1]` | pins one entry at a version, the newest by default, and [never at an older one](../versions/#never-backwards) unless `BACK=1` |
| `release [SHA=<commit>] [RELEASE_DRY=1]` | in a repository whose include [says this task releases it](#releasing), releases main's head as the next `v<YYYYMMDD>.<NN>`: asks [`proven`](../proven/) that the proof passed on the commit, names it with [`release`](../release/), and publishes it with `gh`, which tags it |
| `lint:workflows` | `actionlint` at the version `versions.yaml` pins (an `actionlint` entry of kind `module`), and a failure for an action used by a tag rather than a commit; nothing is printed on a pass. [It goes in the task CI runs](#put-lintworkflows-in-what-ci-runs) |
| `consumers` | the [`consumers`](../consumers/) report, for a repository with a `consumers.toml` |

## What a repository states

Only what differs, as the include's `vars`:

| var | meaning | when absent |
|---|---|---|
| `RELEASE` | how the repository is released, a map: see [Releasing](#releasing) | `release` is refused: nothing says how the repository is released |
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

## A repository with a bump of its own

Four of seven repositories have a command that moves some entries with rules `bump` does not
have (website's `sources:bump`, spin-machine's `spin-machine-dev bump`, spin-ami's
`spin-os upgrade`). Such an entry says so in `versions.yaml`, with
[`bump: <the command>`](../versions/reference/#an-entry-another-command-moves), and
`go tool task bump NAME=<that entry>` refuses and names the command. A command that shells out to
`versions bump` or `versions refresh` passes [`-by`](../versions/reference/#an-entry-another-command-moves)
with its own name, so the field that names it does not refuse it too. The rule every one of those
commands had in common is everybody's now: [no bump moves a pin backwards](../versions/#never-backwards).

## Releasing

Every repository that includes the tasks has a `release` task, and in most of them it must do
nothing: spin-machine is released by a workflow of its own that builds, signs and then tags, and
spin-ami cuts no release at all. So a repository states how it is released, in the include, and
the task refuses everywhere it is not told that it is the one that releases.

### A repository that releases with this task

```yaml title="Taskfile.yml"
includes:
  common:
    taskfile: '{{.GO_TOOLS_TASKS}}'
    flatten: true
    vars:
      RELEASE:
        map: {proof: ci.yml, jobs: Gate, files: _output/release/*.tar.gz}
```

| key | meaning |
|---|---|
| `proof` | the workflow whose pass proves a commit can be released |
| `jobs` | the jobs that pass must have run, by the start of their display name (`Gate`, not the job's key) and comma-separated |
| `files` | files, or globs of them, the release carries; without it a release is its tag and its notes |

Do not copy `ci.yml` and `Gate` because the repository has them. They are not "the check a pull
request needs": they are the statement that `go tool task release` publishes this repository's
releases, and most repositories have a `ci.yml` with a `Gate` job and are released some other way.

### A repository that releases, but not with this task

This task assumes a release is a tag and files that already exist. A release that is built from
its tag - by GoReleaser, as an image, with a signature made after the build - is not one it can
cut: it publishes as it tags, so the latest release would have no artefact until the build
finished, and none for good when the build failed. spin-machine, spin-ami, devcontainer-cli and
spin release that way, each by a workflow of its own. Such a repository says which:

```yaml title="Taskfile.yml"
    vars:
      RELEASE:
        map: {elsewhere: release.yml}
```

`elsewhere` is the file under `.github/workflows` that releases the repository. It is a key of
the Taskfile, and not a comment beside the include, so that a check can read it. The task refuses
with it:

```
task: release: this repository releases, but not with this task: release.yml does. Its Taskfile says so (RELEASE's elsewhere), and this task releases nothing here.
```

### A repository that does not release

```yaml title="Taskfile.yml"
    vars:
      RELEASE:
        map: {none: "its outputs are copied at a commit, which consumers.toml lists"}
```

`none` is a sentence for a person, and the task refuses with it. A repository that states no
`RELEASE` at all is refused too, and told to state one of the three. `elsewhere` and `none` win
over the other keys, and `proof` without `jobs` proves nothing and is refused.

### Why a map

Under Task 3.54.0 a var given on the command line, or set in the environment, reaches an included
task wherever the include states none of that name. Were the proof and the jobs two plain vars, a
repository that set neither would refuse only for want of them, and two words on a command line
would publish an empty release, marked latest, in a repository released by its own workflow:
spin-machine and spin-ami both have a `ci.yml` with a `Gate` job.

The command line and the environment hold strings. `RELEASE` is a map, which no string is, and
where the include states one it wins over both. So no command line and no environment turns a
repository that states nothing, `elsewhere` or `none` into one this task releases, and none changes the
proof, the jobs or the files of one that does: `internal/tasks/tasks_test.go` runs each of those
under Task.

### Cutting one

A release is cut by a person: `go tool task release`, with `gh` logged in. go-tools' own
`release.yml` runs the same task once its binaries are built, with the run's `GH_TOKEN`.

It is public the moment the task returns: `gh release create` publishes the release, marked
latest, and makes its tag in the same call. There is no draft to look at first, which is what
`RELEASE_DRY=1` is for. And the tag is made with the token the task ran with: a person's token
starts every workflow of the repository that has `on: push: tags`, where a workflow's own
`GITHUB_TOKEN` starts none.

`go tool task release RELEASE_DRY=1` is the release without the release. It fetches, asks
`proven`, names the commit and lists the files, so it refuses what a release would - a
commit the proof has not passed on, one released already, a file that is not there - and then
prints the name and stops before `gh release create`:

```
RELEASE_DRY: 0123abc would be released to spin-stack/brand as v20261122.01, and is not
```

It is how a repository that has just taken the tasks proves its `proof` and `jobs`
on a main that is proven and not yet released. It reads `origin/main`, and says nothing about a
branch.

## Every task is listed everywhere

`go tool task --list` shows all five in every repository that includes them, whether or not they
apply there. The ones that do not apply refuse, before running anything:

| task | where | it says | exit |
|---|---|---|---|
| `release` | the include states no `RELEASE` | `release: this repository's include of the common tasks states no RELEASE, so nothing says how it is released and this task releases nothing. ...` | 201 |
| `release` | `RELEASE` says `elsewhere` | `release: this repository releases, but not with this task: release.yml does. ...` | 201 |
| `release` | `RELEASE` says `none` | `release: this repository does not release. Its Taskfile says why (RELEASE's none): ...` | 201 |
| `consumers` | there is no `consumers.toml` | `no consumers.toml: this repository names nobody that copies from it` | 201 |
| `bump` | no `NAME` was given | `Task "bump" cancelled because it is missing required variables: NAME` | 206 |

## What a failure means

| it says | what happened | what to do |
|---|---|---|
| `include cycle detected between <Taskfile> <--> <Taskfile>` (exit 110), for every task | `GO_TOOLS_TASKS` is empty: Task was run directly, or `go.mod` requires a go-tools from before `v0.20261122.4`, whose `go tool task` does not set it | run `go tool task`; `go get github.com/spin-stack/go-tools@latest` |
| `Found multiple tasks (versions) included by "common"` (exit 203), for every task | the repository's Taskfile defines a common task itself | delete the repository's own: the name has one meaning |
| `No Taskfile found at ".../tasks.yml"` (exit 100), for every task, after a line from `go tool task` that starts `the go-tools release this machine has ... has no tasks.yml` | the release in the cache is from before the tasks shipped, and no newer one could be had: GitHub could not be reached, or `go tool task` was built from a commit that is no release's and the cache named the latest less than ten minutes ago | run it again when `https://github.com/spin-stack/go-tools/releases/latest` can be reached, or ten minutes later |
| `toolcache: v<YYYYMMDD>.<NN>, the release this command is of, is not visible yet: https://github.com/spin-stack/go-tools/releases/latest still answers ...`, for every task | in the first minute or two after a go-tools release, GitHub answers some requests with the release before. `go tool task` asks three more times, 2, 4 and 8 seconds apart, and then runs nothing, since what it would run is from before the release it was compiled from | run it again a minute later |
| `label "runs-on/fleet=.../env=production" is unknown`, from `lint:workflows` | a job runs on one of spin-stack's own runners, and actionlint knows only GitHub's labels | name the labels in [`.github/actionlint.yaml`](#put-lintworkflows-in-what-ci-runs) |
| `task: Failed to run task "versions": exit status 1` or `exit status 2` (exit 201) | an entry is behind (1), or [could not be read](../versions/#an-entry-check-could-not-read) and may be (2). Task's own exit status is 201 for both, and for any task whose command failed | `go tool task bump NAME=<name>`; for 2, read the row: usually a private upstream and no token |
| `versions: there is no actionlint in versions.yaml`, from `lint:workflows` | the task runs `actionlint` at the repository's pin, and there is none | add the `actionlint` entry (kind `module`, source `github.com/rhysd/actionlint/cmd/actionlint`) |
| `Taskfile.yml includes go-tools' tasks, and its task lint:workflows reads actionlint: versions: there is no actionlint in versions.yaml`, from the repository's tests | the same, found by the [versions gate](../versions/#the-gate) before the task is run | the same |
| `versions.yaml pins actionlint, and nothing reads it`, from the repository's tests, in a repository that includes the tasks | `go.mod` requires a go-tools whose gate does not count what the common tasks read (`v0.20261122.4` or before), or the include is not one the gate reads: it is a Taskfile's `includes` entry whose `taskfile` is `{{.GO_TOOLS_TASKS}}`, in either kind of quotes | `go get github.com/spin-stack/go-tools@latest`; never a comment that spells the read |
| `release: this repository releases, but not with this task` or `release: this repository does not release` (exit 201), `no consumers.toml` (exit 201) | a task that does not apply to this repository | nothing: see above |
| `release: this repository's include of the common tasks states no RELEASE` (exit 201) | the Taskfile does not say how the repository is released. `GATE_WORKFLOW` and `GATE_JOBS`, where an include still has them, are read by nothing | state `RELEASE` in the include: [Releasing](#releasing) |
| `consumers.toml: scope is no field of the table`, from `consumers` | the table has `scope = "..."`; it is `scopes`, a list | `scopes = ["@fontsource", "@fontsource-variable"]` |
