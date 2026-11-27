---
title: ghatrace
description: Where a GitHub Actions run's time went - the critical path, each job's wait for a runner, its steps and the segments inside them, and whether each worked or waited - as the run's summary, a page that opens in a browser, and a trace for Perfetto, with nothing hosted.
---

A run's page says how long each job took. It does not say which jobs the wall time is made of,
how long each one waited for a runner, which part of a twelve-minute step is the slow one, or
whether that part was working, waiting on a disk, short of a CPU, or asleep.
All of that is in GitHub's API or in the job itself. A hosted backend - an OpenTelemetry
collector, Honeycomb, Datadog - would draw it, and ghatrace draws it from the run alone:

```sh
go get -tool github.com/spin-stack/go-tools/cmd/ghatrace
go tool ghatrace run                          # in the run's last job
go tool ghatrace span -name e2e:up -- task e2e:up   # inside any step, to time a part of it
go tool ghatrace sample                       # a step near a job's start: its machine, every second
```

## What it looks like

This is the page `run` wrote for one of this repository's own gates, opened: one job, its steps,
golangci-lint as a span inside Lint, inside Test every package and every test of a second or more,
and under the job its machine, sampled every second. Hover a bar for what its command used and
what the machine did meanwhile. Its `go test` took 2m04s and 7m20s of CPU, with 3.6 of the
runner's 4 CPUs busy and something waiting for a CPU 53% of the time: working, and short of CPUs.
Its mutate step took 1m29s with 0.7 of 4 CPUs busy: mostly waiting, with three CPUs to spare.
Whether that is worth changing is the repository's call; the trace is what shows it. A test writes
this page from that run, so it is always what the code draws
([open it on its own](example.html#open)).

<iframe src="example.html#open" title="ghatrace's page for a run of go-tools' gate" style="width: 100%; height: 720px; border: 1px solid var(--sl-color-gray-5); border-radius: 6px;" loading="lazy"></iframe>

## What `run` writes

`run` reads its own run (`$GITHUB_RUN_ID`, or `-run` with an id or a run's URL) with
`$GH_TOKEN` or `$GITHUB_TOKEN`, which needs `actions: read`. It writes three things:

- **The run's summary** (`$GITHUB_STEP_SUMMARY`, or stdout off a runner): the wall time, the
  time on runners and as job-minutes rounded up the way GitHub bills its own runners, the time
  spent waiting for runners and the longest wait, the critical path, and the ten slowest steps and
  spans that hold no other - what holds others says again what they say. Beside each, the CPU time
  its command used, when it was a span's command, and its machine meanwhile, when the job was
  sampled: how many CPUs were busy, how long tasks stalled for a CPU, for IO and for memory, and
  the disks. A step that took 35 minutes beside `0.3 of 64` CPUs busy and nothing stalled was
  waiting on something that is not this machine.
- **`trace.html`** in `-out` (`_output/ghatrace`): one page with nothing to fetch. Every job is a
  row with its wait and its run on one time axis, the critical path in its own colour; a job
  opens to its steps, and a step to its spans. Under a sampled job, a line for the share of its
  machine's CPUs busy and one for the time waiting on IO; a bar's tooltip says what its command
  used and what the machine did while it ran.
- **`trace.json`** in `-out`: the same run as Chrome trace events, for
  [ui.perfetto.dev](https://ui.perfetto.dev). A job is a process with its steps on one track and
  its spans on as many as it takes, which is where to zoom into a step or compare shards. A sampled
  job's machine is counters under it - `CPU time %` (busy, iowait, steal), `stalled %` (each
  pressure), `disk MB/s` (read, written), `disk busy %` - and a slice's args say what its command
  used and what the machine did meanwhile.

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

**What a span's command used.** `span` and `gotest` wait for their command, and the kernel hands
the waiting process what the command used, which they record with the command's span (`usage`;
gotest's packages and tests have none, as go test runs them in processes of its own): its CPU
time, user and system, and on Linux the resident set of its largest process and the bytes it read
from a disk and wrote to one. Wall time far above the CPU time is a command that waited. What is
counted is the command and every process under it that something under it waited for. A process
it started and left running is not, and neither is a container's: `docker run` asks the
container runtime, whose children the container's processes are, so a span of `docker run` shows
almost no CPU whatever the container did - its machine's samples show it. Bytes read are what came
from the disk, not what the page cache already held; bytes written are counted as they reach the
page cache, whether or not the disk has them yet; a file on tmpfs is neither.

## The machine under a job

The time says a step was slow, not why. `ghatrace sample` reads the job's machine every second
(`-every`) into the same file as its spans, from what Linux counts from boot - so what the machine
did between two samples is their difference, exact whatever happened between them:

- **CPUs busy**: the share of all CPUs' `/proc/stat` ticks that were neither idle, nor iowait,
  nor steal, said as a number of CPUs: `0.3 of 64` is a machine with one process working a third
  of the time, not a busy one. **iowait** and **steal** are shares of the same ticks: idle with IO
  outstanding, and taken by the hypervisor. Each state's ticks are its own difference: iowait can
  go back, and then counts nothing.
- **Stalled on CPU, IO, memory**: `/proc/pressure`'s `total` - the microseconds in which at least
  one task waited for a CPU, for IO, or for memory (`some`), and, for IO and memory, in which every
  task that could run waited (`full`) - as a share of the time. A step stalled on IO most of the
  time is waiting on a disk; one stalled on CPU is short of CPUs.
- **Disk**: `/proc/diskstats`, for each physical disk - one with a device under `/sys/block`, which
  a partition, a loop device or a device-mapper one is not, as they would count the same bytes
  again: megabytes (10⁶ bytes) read and written a second, summed over the disks, and the share of
  the time the busiest one had a request in flight.

A step of a job's own starts it, near the start, and it returns at once: each step is a process
the runner waits for, and a process a step leaves running lives on through the steps after it.
The sampler is one such, with nothing to say to the step, and the runner ends it when the job
ends, as it ends every process the job started (they carry the job's `RUNNER_TRACKING_ID`; the
job's log says `Terminate orphan process: ... (ghatrace)`). Each sample is written as it is taken,
so nothing waits on that end, and a reader of the file while the sampler writes leaves out a last
line not yet whole. A job that starts it twice has one sampler; it stops on its own after six
hours (`-for`), should nothing end it, or when a sample cannot be written.

What a machine does not have is not sampled, and `sample` says so: a kernel without PSI has no
pressure, a container no disk of its own. Off Linux there is nothing to read, and off a runner
(no `$RUNNER_TEMP` or `$GHATRACE_SPANS`) nowhere to write: `sample` says which and the step goes on.
A sample never fails a job. A sample's line is `{"sample": {...}}`, which a ghatrace from before
samples reads as a span of no job and draws nothing of.

## In a workflow

The last job needs every other one and runs whether they passed or not:

```yaml
jobs:
  lane:
    # ...
    steps:
      # ... the checkout and setup that go tool needs, then:
      - run: go tool ghatrace sample
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
