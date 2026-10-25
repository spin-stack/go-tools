---
title: deadgate
description: Fails on a function the repository's binaries link but never call, and on a package none of them imports, unless one of two lists says why - the allowlist for what is correct for ever, the pending list for a deletion somebody owes - and fails on an entry that matches nothing.
---

A component with no caller is a liability, and a list of them written by hand goes stale inside
one increment. `deadgate` computes the list: every function the shipped binaries link and never
call, by `golang.org/x/tools/cmd/deadcode`, and every package no binary imports. Each finding is
then a decision somebody has to write down.

```sh
go tool deadgate -allow hack/deadcode-allow.txt -pending hack/deadcode-pending.txt \
  -deadcode .tools/bin/deadcode -roots ./cmd/... -skip ./cmd/proxy -packages ./internal/...
```

The roots are every `main` package under `-roots` except those `-skip` names, and only the
binaries: `deadcode -test` would make every test's own subject reachable, answering "is this
called by anything at all" rather than "does a binary reach it". The roots are built first,
because deadcode exits 0 having loaded a broken program, with the broken package simply absent: a
tree that does not compile would produce a *shorter* report, the direction that hides findings.

## Two lists, one way

A finding is explained by a line in one of two files, `<symbol>  # why` or `package <path>  # why`,
the reason mandatory:

```text title="hack/deadcode-allow.txt"
# Unreachable, and correct for ever.
internal/storage/testinfra.Fake  # a test's front-end; no binary should reach it
```

```text title="hack/deadcode-pending.txt"
# Unreachable, and a deletion nobody has finished.
internal/old/migrate.Run  # spin#412 removes the last caller; delete with it
```

Moving the second list's lines into the first would make the gate green on the day it is turned
on, at the cost of the allowlist's meaning. Two lists keep the claim honest, and the gate fails on:

- a finding in neither list;
- an entry in either list that matches no finding: the code was deleted or something calls it now,
  and a list that keeps the line goes on excusing something nobody does;
- a symbol in both lists, which claims it is fine for ever and must be deleted;
- an entry with no reason.

A pinned count was rejected: deleting one finding would buy the right to add another. On a green
run - the only kind anybody has once this is a gate - the pending list is printed whole, so the
debt is read where it is seen.

## What it cannot see

Two blind spots make the report a floor and not a proof. RTA marks every method of a type that
reaches `reflect` as live, so absence from the report is not evidence of a caller. And a package no
binary imports is not in the program deadcode reads at all, so the package pass over `-packages`
reports each of those as `package <path>`: coarse on purpose, "nothing links this at all" being a
blunter statement than "this function is unreachable".

A binary `-skip` names is outside the gate with its own packages: deadcode ran out of memory over
one repository's custom Caddy build, and a symbol under it is unreachable *from this analysis*,
which is a statement about the scope and not a finding. What the report says holds for one
GOOS/GOARCH/build-tag configuration, the one it runs in.

## In a Taskfile

The deadcode binary is built against the `x/tools` the module compiles with, or it answers about
another program than the tree builds:

```yaml title="Taskfile.yml"
tasks:
  tools:deadcode:
    cmds:
      - GOBIN={{.TOOLS_BIN}} go install golang.org/x/tools/cmd/deadcode@{{.XTOOLS_VERSION}}
  deadcode:
    deps: [tools:deadcode]
    cmds:
      - go tool deadgate -deadcode {{.TOOLS_BIN}}/deadcode -roots ./cmd/... -skip ./cmd/proxy -packages ./internal/... -allow hack/deadcode-allow.txt -pending hack/deadcode-pending.txt
```
