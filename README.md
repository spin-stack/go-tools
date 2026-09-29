# go-tools

[![CI](https://github.com/spin-stack/go-tools/actions/workflows/ci.yml/badge.svg)](https://github.com/spin-stack/go-tools/actions/workflows/ci.yml)

The Go the spin-stack repositories build with, and hold their code to: tools a repository runs
from its go.mod (`go tool <name>`), each a package with a command beside it. Nothing here is
linked into what any of them ships but versions' reading half, which spin's binaries use to read
the pins they carry.

## versions

`versions.yaml` at a repository's root is every input it pins from outside it - a base image, an
upstream tarball, a repository's commit, the day a package archive is read as of - written once.
The package `versions` reads it (`Load`, or `Parse` for a program that embeds the file), and
`versions/upstream` says where each entry stands against its upstream and bumps one, rewriting
its two lines and nothing else; `upstream.Pinned` is an entry's download, taken only when it is
the bytes the entry pins. The split is so that a program that ships its pins links only
the reading half: reading one runs nothing and reaches nothing. `cmd/versions` is the command a Taskfile runs;
`versions.Gate`, run from a test, is what fails when a pin is written anywhere else.

A repository takes the command as a Go tool, at the version its go.mod pins:

    go get -tool github.com/spin-stack/go-tools/cmd/versions

    go tool versions args debian kernel   # --build-arg DEBIAN_IMAGE=... KERNEL_VERSION=... KERNEL_SHA256=...
    go tool versions env  debian kernel   # the same as KEY=value lines
    go tool versions version kernel
    go tool versions ref debian           # debian:trixie@sha256:...
    go tool versions check                # what is behind; exits 1 when anything is
    go tool versions bump kernel [7.2.9]  # resolve the pin, rewrite the entry

and holds itself to the file from a test:

```go
func TestVersionsYAMLIsTheOnlyPin(t *testing.T) {
	g := versions.Gate{Root: "..", Elsewhere: []string{"go.sum"}}
	if err := g.Check(); err != nil {
		t.Error(err)
	}
}
```

The gate reads the files git lists - tracked, or untracked and not ignored - and fails on a sha256
or a commit written outside versions.yaml (but in the files `Elsewhere` names, tests, `_output/`,
dot-directories but `.github`, and an action a workflow or a composite action runs by its commit), on a Dockerfile `ARG` that is a pin with a default or with no entry to hand
it, on an entry nothing reads, and on one a bump could not rewrite in place. What reads an entry
is `versions args|env|version|ref <name>...` or `{{.VERSIONS}} ...`, in any file, a test's
included; `Gate.Reads` says otherwise.

### An entry

```yaml
- name: kernel
  kind: download
  source: https://cdn.kernel.org/pub/linux/kernel/v{major}.x/linux-{version}.tar.xz
  version: 7.2.8
  pin: 12e8d5a973d1...      # its sha256
  track: kernel-stable
  note: what a bump has to be checked with.
```

| kind | version | pin | build arguments |
|---|---|---|---|
| `image` | the tag | its digest | `NAME_IMAGE` (the reference by digest) |
| `git` | a tag or a branch | the commit; `archive:` the sha256 of `git archive`, bumped by hand | `NAME_VERSION`, `NAME_COMMIT`, `NAME_ARCHIVE_SHA256` |
| `download` | put in `source`'s `{version}` (and `{major}`) | the file's sha256 | `NAME_VERSION`, `NAME_SHA256` |
| `date` | a point in time | none | `NAME` |
| `module` | a Go program's version; `source` its package path, for `go install <source>@<version>` | none: the checksum database holds it | `NAME_VERSION` |
| `pypi` | the package's version; `source` its name | none: a test's tool only | `NAME_VERSION` |

| track | the newest is |
|---|---|
| `github-release [repo]` | the repository's latest release (`source`'s by default), with a `v` only when the version has one |
| `tags <repo>` | the highest release-numbered tag of a git repository; for a download, the highest whose file is published |
| `commit <repo> <branch>` | the commit a branch is at, for a download whose version is a commit: behind only when the file changed |
| `go` | the newest stable Go, spelled as the tag is (`1.27.1-trixie`) |
| `kernel-stable` | the newest release of the same stable series; a series that is over is a decision |
| `digest` | the same tag: behind when it names another image |
| `branch` | the same branch: behind when it is at another commit |
| `pypi` | the package's newest on PyPI |
| `page <url>` | the first file a page links to named as `source`'s, `{version}` the version |
| `today` | today's date |
| `follows <name>` | another entry's to say: check reports it for a person to read |

`GITHUB_TOKEN`, when set, is sent to api.github.com, which answers sixty unauthenticated requests
an hour.

## mutate

`go tool mutate -base origin/main` breaks what a change touched - a comparison inverted, a field
no longer written, a bound moved by one, in each function the diff reaches - and asks the tests
whether they notice. An edit no test refuses is either a behaviour nothing holds, answered with
a test, or one that means nothing, answered with `mutate-exempt: <reason>` on the line; a
function only a lane this run cannot reach (root, KVM, a cloud account) names the lane's test
with `mutate-lane: <TestName>` in its doc comment. `-stale-exempts` fails on a reason over a line
the tool would not break.

A package's edits are built into one test binary and switched on one at a time, and only the
tests whose coverage reaches a line are run for it; a package with no tests is asked through the
packages that import it. What the tests need besides the code is the environment's: spin runs it
under its hack/testpg, which starts one PostgreSQL for every test process.


## ctxlife, testquality, refs

Three gates a repository runs over itself from `task lint`, each failing with what to do:

- `go tool ctxlife [dir...]` fails on a goroutine that uses the context its starter was given and
  is not waited for: it stops working the moment the call that started it returns. A detached
  context (`context.WithoutCancel`), or `ctx-lifetime: <reason>` on the line, answers it.
- `go tool testquality [-allow file] ./...` fails on a test that cannot fail - nothing in it
  reaches `t.Error`, `t.Fatal`, `t.Skip`, an assertion package, or a helper handed the test - and
  on one skipped unconditionally. The allowlist is a ratchet: an entry nothing matches fails too.
- `go tool refs [-files 'AGENTS.md ...'] [-allow file]` fails on a repository path or a `task
  <name>` that the prose an agent loads (AGENTS.md and CLAUDE.md, by default) names and the tree
  does not have; `-allow` lists the ones named on purpose, `<file><TAB><reference>  # why`.
