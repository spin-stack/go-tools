---
title: Getting started
description: Add go-tools to a repository as Go tools, run the gates from a Taskfile, and wire them into CI.
---

Every tool here is a command under `cmd/` that a repository runs with `go tool`, at the version
its `go.mod` pins. There is nothing to install globally, and nothing to keep in sync between
machines.

## Add the tools you want

```sh
go get -tool github.com/spin-stack/go-tools/cmd/mutate
go get -tool github.com/spin-stack/go-tools/cmd/testquality
go get -tool github.com/spin-stack/go-tools/cmd/versions
```

Each adds a `tool` line to `go.mod`, and `go tool <name>` runs it. Moving to a newer go-tools is
one command, reviewed like any other dependency bump:

```sh
go get github.com/spin-stack/go-tools@<commit>
```

## Run them where developers already run checks

The spin-stack repositories put the quick gates in `task lint` and the test-driven ones after
`task test`, so CI runs exactly what a developer runs locally:

```yaml title="Taskfile.yml"
tasks:
  lint:
    cmds:
      - golangci-lint run ./...
      - go tool ctxlife
      - go tool testquality ./...
      - go tool refs
      - go tool mutate -stale-exempts
      - go tool sfw check
      - go tool vulngate ./...

  mutate:
    vars:
      BASE: '{{.BASE | default "origin/main"}}'
    cmds:
      - go tool mutate -base {{.BASE}} -max 200
```

```yaml title=".github/workflows/ci.yml"
steps:
  - uses: actions/checkout@...
    with:
      fetch-depth: 0 # mutate diffs against the branch point
  - run: task lint
  - run: task test
  - run: task mutate BASE="origin/${{ github.base_ref }}"
```

The [mutate page](../mutate/#running-it) has the full CI step, which also handles pushes
to `main`.

## Which tool for which problem

| you want to know | tool |
|---|---|
| whether the tests would catch a bug in what this change touched | [mutate](../mutate/) |
| whether some tests can't fail at all | [testquality](../testquality/) |
| whether a goroutine is cancelled when its caller returns | [ctxlife](../ctxlife/) |
| whether the agent instructions still describe this tree | [refs](../refs/) |
| whether the code calls a vulnerable function that has a fix | [vulngate](../vulngate/) |
| where every pinned image, tarball and commit is, and which are out of date | [versions](../versions/) |
| whether every npm install goes through Socket Firewall, and every workflow through a task | [sfw](../sfw/) |
| what a release was built from, signed | [provenance, attestgate, sbom](../supply-chain/) |

## Libraries

Some of these are also packages another repository imports: `versions` (reading
`versions.yaml`), `versions/upstream`, `versions/gate`, `provenance`, `attestgate`, `sbom` and
`trustroot`. The rest is `internal/`. Only two are linked into anything spin-stack ships:
`versions`, which spin's binaries use to read the pins they carry, and `trustroot`, which they
verify release signatures against.
