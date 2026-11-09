---
title: ghatrace
description: Where a GitHub Actions run's time went - the critical path, each job's wait for a runner, its steps and the segments inside them - as the run's summary, a page that opens in a browser, and a trace for Perfetto, with nothing hosted.
---

A run's page says how long each job took. It does not say which jobs the wall time is made of,
how long each one waited for a runner, or which part of a twelve-minute step is the slow one.
All of that is in GitHub's API or in the job itself. A hosted backend - an OpenTelemetry
collector, Honeycomb, Datadog - would draw it, and ghatrace draws it from the run alone:

```sh
go get -tool github.com/spin-stack/go-tools/cmd/ghatrace
go tool ghatrace run                          # in the run's last job
go tool ghatrace span -name e2e:up -- task e2e:up   # inside any step, to time a part of it
```

## What `run` writes

`run` reads its own run (`$GITHUB_RUN_ID`, or `-run` with an id or a run's URL) with
`$GH_TOKEN` or `$GITHUB_TOKEN`, which needs `actions: read`. It writes three things:

- **The run's summary** (`$GITHUB_STEP_SUMMARY`, or stdout off a runner): the wall time, the
  time on runners and as job-minutes rounded up the way GitHub bills its own runners, the time
  spent waiting for runners and the longest wait, the critical path, and the ten slowest steps and
  spans that hold no other - what holds others says again what they say.
- **`trace.html`** in `-out` (`_output/ghatrace`): one page with nothing to fetch. Every job is a
  row with its wait and its run on one time axis, the critical path in its own colour; a job
  opens to its steps, and a step to its spans.
- **`trace.json`** in `-out`: the same run as Chrome trace events, for
  [ui.perfetto.dev](https://ui.perfetto.dev). A job is a process with its steps on one track and
  its spans on as many as it takes, which is where to zoom into a step or compare shards.

**The critical path** is the chain of jobs the wall time is made of. The API does not say what a
job needs, but a job is queued the moment the last job it needs finishes, so the chain is read
back from the time: the job that finished last, then the job that finished last by the time it
was queued, and so on. A job queued more than ten seconds after anything finished starts the
chain: something outside the run held it, such as an approval.

## Inside a step

The API times steps, to the second, and nothing inside them. Three things fill a step in.

**A composite action's own steps, from the log.** Every line of a job's log starts with its time,
and GitHub starts each step it runs - a composite action's included, which the API does not list -
with a `##[group]Run ...` line. `run` reads each finished job's log (`-logs=false` reads none) and
draws a step's own steps under it, each until the next starts. Nothing in the workflow changes: a
setup action that takes a minute in every job shows which of its parts takes it.

**Go's tests, from `go test -json`.** `ghatrace gotest` runs a go test command with `-json` put
after its `go test` - never through `GOFLAGS`, which every go command a test runs would inherit
and answer in JSON. A wrapper in front of go test - a script that starts a database first - is
run as it is, and its go test is given `-json` by whoever wrote it:

```sh
go tool ghatrace gotest -- go test -v -count=1 ./internal/e2e/...
go tool ghatrace gotest -name unit -min 2s -- go test -race ./...
go tool ghatrace gotest -- with-db.sh go test -json ./store/...
```

It writes what go test would have written, with `-v` or without, so a log and whatever reads one
see no difference, and exits as go test did. Each package is a span under the command's (`-name`,
`go test` by default), each test of `-min` or more (a second by default) a span under its
package, and each subtest one under its test: by name rather than by time, since packages and
parallel tests run side by side.

## Spans: the parts you name

`ghatrace span` runs a command and
records it - its name, start, end, exit code and the job's runner - to `$GHATRACE_SPANS`, or
`$RUNNER_TEMP/ghatrace-spans.jsonl` on a runner. Off a runner it only runs the command, so a task
can wrap its parts and a laptop sees no difference. It exits as the command did, and hands an
interrupt on to it, so a cancelled job still records where it was.

A Go program that knows its own parts - a runner of lanes, a test harness - records them itself
with the package `github.com/spin-stack/go-tools/ghatrace`: `ghatrace.Record(ghatrace.Span{Name:
..., Start: ..., End: ..., Exit: ...})` writes to the same file, and does nothing off a runner.
Parts that run side by side in one step overlap, and by the time alone a part of one is inside the
other too: a span whose `Parent` names the span it belongs to goes under that one.

A job uploads that file as an artifact whose name starts with `ghatrace-`, and `run` reads every
such artifact of the run. A span goes under the step that ran it, in the job whose runner
recorded it, and inside any span that contains it.

## In a workflow

The last job needs every other one and runs whether they passed or not:

```yaml
jobs:
  lane:
    # ...
    steps:
      # ... steps that run `go tool ghatrace span -name ... -- ...` somewhere inside
      - if: ${{ !cancelled() }}
        uses: actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # v7.0.2
        with:
          name: ghatrace-${{ github.job }}-${{ strategy.job-index }}
          path: ${{ runner.temp }}/ghatrace-spans.jsonl
          if-no-files-found: ignore

  trace:
    needs: [lane]
    if: ${{ always() }}
    runs-on: ubuntu-slim
    permissions:
      actions: read
    steps:
      - uses: actions/checkout@...
      - uses: actions/setup-go@...
      - run: go tool ghatrace run
        env:
          GH_TOKEN: ${{ github.token }}
      - uses: actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # v7.0.2
        with:
          path: _output/ghatrace/trace.html
          archive: false
      - uses: actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # v7.0.2
        with:
          path: _output/ghatrace/trace.json
          archive: false
```

With `archive: false` each file is an artifact of its own, named as the file, and downloads as
itself rather than a zip. A workflow whose last job already waits for the others - a gate that
reads every lane's answer - can run ghatrace as that job's last steps instead, and save a runner.

One run is one trace: a workflow that another triggers, or two workflows on one push, are traces
of their own.
