---
title: proven
description: Asks GitHub what CI already proved about a commit - which run answers for its tree, what a run on main is of, whether a release may be made, how long each test took, which failed tests are flaky - and writes the answer where the workflow reads it.
---

A workflow that decides something from what GitHub says - which commit to diff against, whether
the lanes already ran on these bytes, whether a commit may be released - is a function over runs
and commits. Written in a shell step it has no test but the run itself, and the run on main is the
one place a wrong answer costs the most: a base chosen two days back made `mutate` diff 332
commits, the lane timed out, and nothing on main could pass again until somebody read the script.
`proven` is those functions in Go, with their tables, and one small client for the API.

```sh
go tool proven tree|base|release|times|flakes [flags]
```

Every command takes the repository from `-repo` or `$GITHUB_REPOSITORY`, the commit from `-sha`
or `$GITHUB_SHA` or `HEAD`, the token from `$GH_TOKEN` or `$GITHUB_TOKEN`, and the API from
`$GITHUB_API_URL`. The gate is `-workflow`, `ci.yml` unless told otherwise, and only its runs on
a schedule or a dispatch answer for a tree (`-events`). What a workflow reads next goes to
`$GITHUB_OUTPUT`, and to stdout outside one.

## A run's answer is a tree's

A pull request's head, rebased onto a main that did not move, merges as a commit with the same
tree: the same bytes, another sha. The lanes that passed on the head passed on the merge, and do
not need an hour to say so again. So every answer here is found by tree: a run on the commit
itself, or on any commit with its tree, the newest that finished first. A cancelled run finished
nothing and answers for nothing; a run still going is not an answer yet.

A commit's tree comes from the clone when the clone has it, which is free, and from the API when
it does not - a pull request's head is nowhere in a checkout of main.

## tree

```yaml title=".github/workflows/kvm.yml"
- id: proven
  env:
    GH_TOKEN: ${{ github.token }}
  run: go tool task kvm:proven   # go tool proven tree
```

`proven=true` when the gate passed on the commit's tree, `proven=false` when it did not or never
finished on it; a job that `needs` this one runs only the lanes that run did not. The step passes
either way: the answer is the output, not the exit code.

## base

```yaml title=".github/workflows/ci.yml"
- id: proven
  env:
    GH_TOKEN: ${{ github.token }}
    GITHUB_REF: ${{ github.ref }}
  run: go tool task ci:base      # go tool proven base
```

What a run on main is of. The newest of main's commits (`-depth` of them, first-parent) that a
passed run ran on - by sha, or by tree - is the base: `proven=true` when it is main's head, since
nothing new is left to run, else `base=<commit>`, the diff `mutate` is asked to break and the
tests a change reaches are found against. When no commit of main has a run among the last
`-limit`, the base is the commit before. A run on another branch (`-ref` not `-main`'s) is of that
branch against main: `base=origin/main`.

By tree and not by main's own runs: asked only of runs on main, the base was the last of them
that passed, and every one that failed since kept it there, while every commit between had passed
on its pull request's branch as the same tree.

## release

```yaml title=".github/workflows/release.yml"
- env:
    GH_TOKEN: ${{ github.token }}
  run: go tool task release:green   # go tool proven release
```

Refuses (exits 1) a commit the gate did not pass on, by tree, and a commit the lanes that run on
every push to main (`-also`, `kvm.yml`; empty asks only the gate) did not pass on, by sha: their
newest run on it, since a re-run replaces a conclusion and a run cancelled for want of a machine
is dispatched again. Each answer is a line, the refusals as `::error::` for the job's log. A commit
no run reached is released by dispatching the gate on main first.

## times

```yaml title=".github/workflows/ci.yml"
- id: times
  env:
    GH_TOKEN: ${{ github.token }}
  run: go tool task ci:times     # go tool proven times
```

`times=<lines>`, each `<seconds> <test>` as `go test` printed them in the logs of the jobs whose
names start with `-jobs` (`e2e-verify`), from the newest run that passed and logged any - a run
proven by another ran no lane. A split of the tests over machines reads it, so the shards are even
by measured time rather than by count; with none, it splits by name.

## flakes

A test that failed and passed on the same tree is a flake by definition: the same bytes, another
answer. One that failed on every run of every tree it failed on was the code, broken then. The
difference is what a rerun answers and what it does not, and reading it off the last few days'
runs by hand is where a flake stays a rumour.

```sh
go tool proven flakes -limit 100          # the last 100 runs of ci.yml, and of -also (kvm.yml)
```

For every run that failed, the tests its kept logs say failed: the run's artifacts first - a
lane's logs, which a fleet keeps only when the lane failed - and, when it kept none, its failed
jobs' logs. Each failed test is then judged by its trees:

```text
| test | failed | of runs | branches | verdict |
| --- | --- | --- | --- | --- |
| ci.yml: TestARunningWorkspaceResumesFromASnapshotsMemory | 3 ([401](...) [400](...) [399](...)) | 160 | e2e-hosts, ci/three-shards | flaky: passed on the same tree |
| kvm.yml: TestAnImageRewrittenInTheBucketIsNotCloned | 3 ([403](...) [402](...) [398](...)) | 160 | main | red: failed on every run of its trees |
flaky more than once, for hack/quarantine.txt or a fix: ci.yml: TestARunningWorkspaceResumesFromASnapshotsMemory
```

The table goes to the run's summary when there is one. A cancelled run finished nothing and says
nothing; a run whose logs are gone counts as a run the test did not fail in only when it passed.
Run it on a schedule beside the gate, so the list is read where the reruns are paid for.
