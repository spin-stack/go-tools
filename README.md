# go-tools

[![CI](https://github.com/spin-stack/go-tools/actions/workflows/ci.yml/badge.svg)](https://github.com/spin-stack/go-tools/actions/workflows/ci.yml)

The Go the spin-stack repositories build with, and hold their code to: tools a repository runs
from its go.mod (`go tool <name>`), each a command under `cmd/`. Only `versions`,
`versions/upstream`, `trustroot`, `provenance` and `attestgate` are packages another repository
imports; the rest is
`internal/`. Nothing here is linked into what any of them ships but versions' reading half, which
spin's binaries use to read the pins they carry, and the trusted root, which what verifies a
release's signatures checks them against.

## versions

`versions.yaml` at a repository's root is every input it pins from outside it - a base image, an
upstream tarball, a repository's commit, the day a package archive is read as of - written once.
The package `versions` reads it (`Load`, or `Parse` for a program that embeds the file), and
`versions/upstream` says where each entry stands against its upstream and bumps one, rewriting
its two lines and nothing else; `upstream.Pinned` is an entry's download, taken only when it is
the bytes the entry pins. The split is so that a program that ships its pins links only
the reading half: reading one runs nothing and reaches nothing. `cmd/versions` is the command a Taskfile runs;
`gate.Gate` (`versions/gate`), run from a test, is what fails when a pin is written anywhere else.

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
	g := gate.Gate{Root: "..", Elsewhere: []string{"go.sum"}}
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

## provenance, attestgate

`go tool provenance -version <release> -out <file>`, in a workflow run, writes the SLSA v1
provenance predicate of that run - the workflow at its ref as the builder, the repository at its
commit and every versions.yaml pin as the inputs, and no clock - for `cosign attest-blob --type
slsaprovenance1 --predicate <file>` to sign over the files. Package `provenance` is the same, for
a build that adds inputs of its own (`Build`'s extra).

`attestgate.Gate`, run from a test like the versions gate, fails on a workflow step that
publishes (a release action, `oras push`, `gh release upload`, `docker push`, `aws s3 cp`, a task
named in `UploadTasks`) without a step before it in its job that attests (`cosign attest-blob
--type slsaprovenance1`, or a task in `AttestTasks`) unconditionally and with `id-token: write`.
`NotPublished` excuses a step by `<file>/<job>/<step>`, with its reason, and an excuse that matches
nothing fails; `MainOnly` holds release workflows to no push trigger and every job on
`refs/heads/main`. The YAML cannot say which files a glob covered, so the step right before an
upload runs `go tool attestgate files -require provenance [-except checksums.txt] <dir>`, which
fails on a file there with no `<file>.provenance.sigstore.json` beside it.

## trustroot

`trustroot.Load()` is Sigstore's trusted root - the Fulcio authorities, Rekor logs, CT logs and
timestamp authorities a bundle is checked against - embedded as TUF served it, so a signature is
verified with nothing reached over the network. Every repository takes the same root by its
go-tools version: renewing it is `go run ./cmd/trustroot -write` here, a commit, and a go-tools
bump there. `go tool trustroot -check` fails when TUF serves anything else, byte for byte; it
needs the network, and this repository's `task lint` runs it.

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


## ctxlife, testquality, refs, vulngate

Four gates a repository runs over itself from `task lint`, each failing with what to do:

- `go tool ctxlife [dir...]` fails on a goroutine that uses the context its starter was given and
  is not waited for: it stops working the moment the call that started it returns. A detached
  context (`context.WithoutCancel`), or `ctx-lifetime: <reason>` on the line, answers it.
- `go tool testquality [-allow file] ./...` fails on a test that cannot fail - nothing in it
  reaches `t.Error`, `t.Fatal`, `t.Skip`, an assertion package, or a helper handed the test - and
  on one skipped unconditionally. The allowlist is a ratchet: an entry nothing matches fails too.
- `go tool refs [-files 'AGENTS.md ...'] [-allow file]` fails on a repository path or a `task
  <name>` that the prose an agent loads (AGENTS.md and CLAUDE.md, by default) names and the tree
  does not have; `-allow` lists the ones named on purpose, `<file><TAB><reference>  # why`.
- `go tool vulngate [govulncheck flags] ./...` runs govulncheck and fails on a vulnerability the
  code calls and that has a fix, the standard library's included: the answer is the upgrade, and
  for the standard library that is go.mod's `toolchain` line. One only imported, or with no fix,
  is listed and passes. It asks vuln.go.dev for the database, so it needs the network.
