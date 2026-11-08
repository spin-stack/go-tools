---
title: gencache
description: go generate that answers each mockgen directive from a cache when nothing its mock is made from has changed, and runs the rest as go generate would.
---

`go generate` has no cache. Every directive runs every time, one after another, whether or not
anything it reads has changed. A mock written by [mockgen](https://github.com/uber-go/mock)
loads the packages it mocks with their types, so a repository with a hundred mocks pays for a
hundred package loads on every CI run. In spin that was 80 seconds a run, for mocks no change had
touched.

gencache is `go generate` with a cache in front of each `go tool mockgen` directive:

```sh
go get -tool github.com/spin-stack/go-tools/cmd/gencache
go tool gencache ./...                    # the packages, as go generate takes them
GOOS=linux go tool gencache -tags integration,e2e ./...
```

## What it keys a mock by

A mock's bytes depend on what mockgen is given and on what it reads. The key is all of it:

- the directive's words, as go generate expands them, and the package it is in;
- mockgen's build (`go tool -n mockgen`), so a new mockgen version makes every mock again;
- the Go version, `GOOS`, `GOARCH`, `GOFLAGS`, `CGO_ENABLED`, `GOEXPERIMENT` and the module's
  `go` line;
- every package mockgen reads the mock from, read the way mockgen reads it. In package mode that
  is the named package and everything it imports, as `go list` loads them. In source mode it is
  the `-source` file's directory and every package the file imports, test files included,
  because mockgen parses whole directories there.

A package counts by the bytes of every `.go` file in its directory, whatever its build
constraint, named by its import path rather than where it is. A file the build leaves out can
make a mock run again, but never makes one come from the cache when it should not. The mock
being written is never part of its own key.

## What it does with the key

- **The cache has it**: the cached bytes are the mock. When the file on disk is not those bytes -
  deleted, edited by hand, out of date - gencache writes them back. A CI step that checks
  generated code with `git diff` after generating still catches an out-of-date mock.
- **The cache does not have it**: the directive runs through `go generate -run` and its output is
  kept.
- **It cannot be keyed**: the directive runs through `go generate -run` every time, as go generate
  would run it. That is any generator other than `go tool mockgen`, a mockgen flag that reads
  something the key does not cover (`-aux_files`, `-build_flags`, `-copyright_file`,
  `-archive`, `-model_gob`), and every directive in a file that defines a `-command` alias. The
  summary says how many and why.

Packages run side by side (`-jobs`, half the machine by default); a package's directives run in
order. `-jobs 1` is go generate's order, for a directive that reads what another one writes.

## Where the cache is

A directory, `go-tools/gencache` under the user's cache directory by default, or `-cache`. Keys
do not depend on where the checkout is, so a worktree and CI share entries. An entry no run has
used for a week is removed. In CI, keep the directory between runs with `actions/cache`, as you
keep Go's build cache. Without it every run starts cold, which costs what go generate costs.
