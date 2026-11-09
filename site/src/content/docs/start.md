---
title: Getting started
description: Add go-tools to a repository as Go tools, run the gates from a Taskfile, and wire them into CI.
---

Every tool here is a command under `cmd/`, released as binaries of main each time its gate passes
(`v<YYYYMMDD>.<NN>`, named as spin-stack names its releases, and attested as built by
`release.yml`). A repository runs the latest of them through Task, at the version its
`versions.yaml` pins. There is nothing to install globally, and nothing to compile.

## Take Task and the tools

One command in `go.mod`:

```sh
go get -tool github.com/spin-stack/go-tools/cmd/task
```

and Task in `versions.yaml`, pinned by the sha256 of every platform's archive:

```yaml title="versions.yaml"
- name: task
  kind: download
  source: https://github.com/go-task/task/releases/download/v{version}/task_{os}_{arch}.tar.gz
  version: 3.54.0
  pins:
    linux/amd64: 680859dbb4d881a9c72d4d9a8f510825450849af8567deacd7302c01124416fb
    darwin/arm64: 12a69fcf37bb8d5bc9150c44f9a936775b73e59ff044c2c245232f198c135637
  track: github-release https://github.com/go-task/task
```

`go tool task lint` then puts that Task and go-tools' latest release for this machine in the
user's cache - Task held to its pin, the tools to the sha256 their release's `checksums.txt` gives
- unpacks each once, and runs Task with every go-tools command on its `PATH`: a Taskfile says
`mutate -base origin/main`. The first run on a machine takes a second or two. Which release is the
latest is asked at most every ten minutes, and a machine that cannot ask goes on with the one it
has. Task's and the tools' own modules - about 450 together - are in no `go.mod` and no CI cache.

No repository bumps the tools: a release of go-tools is in every repository's next run. What a
repository imports of go-tools as a library - `versions`, `provenance` - is `go.mod`'s, moved by
`go get github.com/spin-stack/go-tools@<commit>` as any dependency is.

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
| what a release was built from, signed | [provenance, attestgate, sbom](../supply-chain/) |

## Libraries

Some of these are also packages another repository imports: `versions` (reading
`versions.yaml`), `versions/upstream`, `versions/gate`, `provenance`, `attestgate`, `sbom` and
`trustroot`. The rest is `internal/`. Only two are linked into anything spin-stack ships:
`versions`, which spin's binaries use to read the pins they carry, and `trustroot`, which they
verify release signatures against.
