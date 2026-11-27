---
title: Getting started
description: Add go-tools to a repository as Go tools, run the gates from a Taskfile, and wire them into CI.
---

Every tool here is a command under `cmd/`, released as binaries of main each time its gate passes
(`v<YYYYMMDD>.<NN>`, named as spin-stack names its releases, and attested as built by
`release.yml`). A repository runs the latest of them through Task, at the version go-tools pins.
There is nothing to install globally, and nothing to compile.

## Take Task and the tools

One command in `go.mod`:

```sh
go get -tool github.com/spin-stack/go-tools/cmd/task
```

`go tool task lint` then puts Task and go-tools' latest release for this machine in the user's
cache - Task held to its pin, the tools to the sha256 their release's `checksums.txt` gives -
unpacks each once, and runs Task with every go-tools command on its `PATH`: a Taskfile says
`mutate -base origin/main`. The first run on a machine takes a second or two. Which release is the
latest is asked at most every ten minutes - and again at once when the cache names one from
before the release `go.mod` requires - and a machine that cannot ask goes on with the one it
has. Task's and the tools' own modules - about 450 together - are in no `go.mod` and no CI cache.

Ctrl+C stops a command and every process it started, on Linux, macOS and Windows, and the command
removes what it wrote to the temporary directory before it exits. So does a SIGTERM sent to the
command alone, as `kill` or a timeout sends it. A second Ctrl+C ends a command at once,
without waiting for that cleanup.

The Task is go-tools' own: [`cmd/task/versions.yaml`](https://github.com/spin-stack/go-tools/blob/main/cmd/task/versions.yaml),
pinned by the sha256 of its archive for every platform a release is built for (Linux, macOS and
Windows, amd64 and arm64), and carried in the command. A repository writes no pin for it, and takes
a newer Task with the go-tools version its `go.mod` requires. A repository whose `versions.yaml` has
a `task` entry runs that one instead, on the platforms it lists: another platform is refused, and
deleting the entry runs go-tools' Task. Upstream releases Task for Windows as a `.zip`, which a
`task` entry's source cannot name beside the `.tar.gz` of the others: on Windows a repository with
the entry is refused, and one without it runs go-tools' Task.

On Windows, `go tool task` runs Task as a process of its own, since Windows replaces no process
with another, and exits as Task did. The commands it puts on Task's `PATH` are `.exe` files of the
release, and a Taskfile names them as it does elsewhere: `mutate`, not `mutate.exe`.

No repository bumps the tools: a release of go-tools is in every repository's next run. What a
repository imports of go-tools as a library - `versions`, `provenance` - is `go.mod`'s. Each
release is also tagged `v0.<YYYYMMDD>.<N>`, a module version, so Dependabot's `gomod` updates
move it as any dependency, and `go get github.com/spin-stack/go-tools@latest` takes the newest.

## Take the tasks every repository has

```yaml title="Taskfile.yml"
includes:
  common: {taskfile: '{{.GO_TOOLS_TASKS}}', flatten: true}
```

gives the repository `versions`, `bump`, `release`, `lint:workflows` and `consumers` as every
other has them. [The common tasks](../tasks/) says what each does, and what a repository states
in the include's `vars`: among them [how it is released](../tasks/#releasing), without which
`release` refuses. Double quotes are the same include, and what a repository formatted by Biome
writes.

## Run them where developers already run checks

The spin-stack repositories put the quick gates in `task lint` and the test-driven ones after
`task test`, so CI runs exactly what a developer runs locally. [GitHub Actions](../github-actions/)
has a repository's whole Taskfile and workflow: every gate, the caches that keep what a run
already answered, and mutate's base on a pull request and on a push.

## Which tool for which problem

| you want to know | tool |
|---|---|
| whether the tests would catch a bug in what this change touched | [mutate](../mutate/) |
| whether some tests can't fail at all | [testquality](../testquality/) |
| whether a goroutine is cancelled when its caller returns | [ctxlife](../ctxlife/) |
| whether the agent instructions still describe this tree | [refs](../refs/) |
| whether the code calls a vulnerable function that has a fix | [vulngate](../vulngate/) |
| where every pinned image, tarball and commit is, and which are out of date | [versions](../versions/) |
| whether every package install is pnpm, through Socket Firewall | [sfw](../sfw/) |
| whether every workflow step is a task a developer can run too | [taskgate](../taskgate/) |
| what a repository would show if it were made public | [exposure](../exposure/) |
| which of a repository's commits each place that copies from it is on | [consumers](../consumers/) |
| what a release was built from, signed | [provenance, attestgate, sbom](../supply-chain/) |

## Libraries

Some of these are also packages another repository imports: `versions` (reading
`versions.yaml`), `versions/upstream`, `versions/gate`, `provenance`, `attestgate`, `sbom`,
`consumers` and `trustroot`. The rest is `internal/`. Only two are linked into anything spin-stack ships:
`versions`, which spin's binaries use to read the pins they carry, and `trustroot`, which they
verify release signatures against.
