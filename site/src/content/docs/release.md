---
title: release
description: Names a repository's next release as spin-stack names releases - v<YYYYMMDD>.<NN>, the day in UTC and which release of that day - and refuses a commit already released.
---

Every spin-stack release is named `v<YYYYMMDD>.<NN>`: the day in UTC, and which release of that
day, always two digits. `release` is where that rule is, so no repository carries a copy.

```sh
release <commit>            # v20261122.03, or exit 1: "<commit> is released already, as v20261122.02"
release -outputs <commit>   # version=, new= and module= for $GITHUB_OUTPUT
```

The name is counted from the clone's tags, so fetch them first. Only releases' names count: a
tag such as `screens-v20261122.01-c2f0602`, a one-digit `v20261122.4` or a module version
`v0.20261122.5` is neither a release of the day nor a reason to refuse the commit.

`-outputs` is for a workflow that releases on its own and may be run again on a commit it
released. It never refuses: a released commit answers its release with `new=false`, so the run
skips the publishing and goes on to mend what the first left undone. `module=` is the release as
a Go module version, `v0.<YYYYMMDD>.<N>`, for a repository whose `go.mod` others name.

[`task release`](../tasks/) is what calls it.
