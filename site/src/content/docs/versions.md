---
title: versions
description: Every image digest, tarball checksum and commit a repository depends on, written once in versions.yaml - read by builds, checked against upstream, bumped in place, and enforced by a test.
---

A repository pins things from outside itself all over the place: a `FROM` image digest in a
Dockerfile, a kernel tarball's sha256 in a script, a commit in a Taskfile, a tool version in CI.
Finding them all when it's time to upgrade is the hard part, and so is knowing which are out of
date.

`versions` puts every one of them in **one file**, `versions.yaml`, and gives you:

- a command that hands each pin to whatever needs it: Docker build args, shell variables, a URL;
- `check`, which says what is behind its upstream;
- `bump`, which resolves the new pin and rewrites that entry's two lines and nothing else;
- a **gate**: a test that fails when a pin is written anywhere else.

## The file

```yaml title="versions.yaml"
- name: kernel
  kind: download
  source: https://cdn.kernel.org/pub/linux/kernel/v{major}.x/linux-{version}.tar.xz
  version: 7.2.8
  pin: 12e8d5a973d1...      # the file's sha256
  track: kernel
  note: what a bump has to be checked with.

- name: debian
  kind: image
  source: debian
  version: trixie
  pin: sha256:...           # the image's digest
  track: digest
```

`version` is what a person reads; `pin` is what makes it exact. `track` says how to find a newer
one, and `note` tells whoever bumps it what to test afterwards. The
[reference](../versions/reference/) lists every kind and track.

## Using the pins

```sh
go get -tool github.com/spin-stack/go-tools/cmd/versions
```

```sh
go tool versions args debian kernel   # --build-arg DEBIAN_IMAGE=... --build-arg KERNEL_VERSION=... --build-arg KERNEL_SHA256=...
go tool versions env  debian kernel   # the same, as KEY=value lines
go tool versions ref  debian          # debian:trixie@sha256:...
go tool versions url  kernel          # the download's URL at its version
go tool versions fetch kernel _cache  # _cache/linux-7.2.8.tar.xz, only as the bytes it pins
go tool versions version kernel       # 7.2.8
go tool versions newer kernel 7.2.9   # 7.2.9: newer than the pin; nothing when not, an error when not of its form
```

A Dockerfile declares the arguments with no default, and the Taskfile passes them:

```dockerfile
ARG DEBIAN_IMAGE
FROM ${DEBIAN_IMAGE}
ARG KERNEL_VERSION
ARG KERNEL_SHA256
```

```yaml title="Taskfile.yml"
vars:
  VERSIONS: go tool versions
tasks:
  image:
    cmds:
      - docker build $({{.VERSIONS}} args debian kernel) .
```

A download a task unpacks is `fetch`'s: it puts the file in the directory, named as its URL names
it, and prints the path. A file already there is checked every time and kept when it is the pin;
one that is not - cut short, or replaced since - is downloaded again, and a download that is not
the pin is refused and leaves nothing behind. So a cache directory needs no rule of its own:

```yaml title="Taskfile.yml"
tasks:
  kernel:
    cmds:
      - tar -xJf "$({{.VERSIONS}} fetch kernel _cache)" -C _build
```

Inside `$( )` a refusal is only the command's: in a script, `set -e` sees it when the path is
assigned on a line of its own, `file=$(go tool versions fetch kernel _cache)`.

### A server having a bad moment

Every download and every lookup is tried again when what failed would not fail the same way the
next time: a 500, 502, 503 or 504, a 408 or a 429, a connection refused, reset or cut halfway
through the body, or a server that sends nothing for 30 seconds. Five attempts, about 4, 8, 16
and 32 seconds apart (a 429's `Retry-After` is waited, up to a minute), and never more than two
minutes of waiting in all. Each wait is said on stderr: the URL, what it answered, how long until
the next attempt and which attempt that is.

An error names the host that answered when a redirect took the request elsewhere: a 504 from
`release-assets.githubusercontent.com` is GitHub's download CDN, not github.com.

GitHub sheds anonymous downloads under load, so in CI set `GITHUB_TOKEN` (a workflow has
`${{ github.token }}`) or `GH_TOKEN`. With one, a release's file
(`https://github.com/<owner>/<repo>/releases/download/<tag>/<file>`) is asked for through
GitHub's API - its release by tag, then its asset as `application/octet-stream` - since a token
on the github.com URL itself is not read. The token goes to `api.github.com` and nowhere else: not
to the CDN the API redirects to, not to github.com, not to any other host.

A 404, a 403 or any other answer that would be the same next time fails at once. A download cut
short is thrown away and fetched again from its first byte, and the pin is checked on the bytes
that arrived whole: a file that is not the pin is refused, not tried again.

## Keeping up

```sh
go tool versions check                # what is behind; exits 1 when anything is, 2 when an entry could not be read
go tool versions bump kernel          # to the newest the track finds
go tool versions bump kernel 7.2.9    # or to a version you name
```

A bump downloads what the new version names and pins its hash; for an image, it asks the registry
for the digest. It builds nothing, and the entry's `note` says what to run before committing.

### Never backwards

A bump does not move a pin to something older than it, named or found:

```
$ go tool versions bump spin v20261024.01
versions: spin is at v20261114.01, and v20261024.01 is older: a bump does not move a pin backwards (versions bump -back, or BACK=1 to the task, moves it there all the same)
```

"Older" is what the tool can order, and nothing else:

- **A version**, by the numbers it starts with: `v20261024.01` is older than `v20261114.01`,
  `7.3` than `7.3.1`, `1.27.4-bookworm` than `1.27.5-bookworm`. This also stops a bump with no
  version named when the upstream's "latest" is older than the pin, and a `refresh` leaves such
  an entry where it is.
- **A commit**, for a `git` entry of a GitHub repository whose versions say neither way - a
  branch, or a branch's commit moved to a tag: the bump asks GitHub's compare whether the pinned
  commit already holds the new one, and refuses if it does. A pin at a commit of main does not
  move to a release cut before that commit. If GitHub cannot be asked the bump is refused, not
  guessed. Two commits on branches that parted are not called backwards.

A version that starts with no number (a date, a word) and a `git` entry of another host are not
ordered, and move as asked. An entry tracked `with` another goes where that one is.
`go tool versions bump -back <name> <version>` is the bump of a person who means it.

`GITHUB_TOKEN` or `GH_TOKEN`, when set, is sent to api.github.com, which answers only sixty
unauthenticated requests an hour, and nothing of a private repository. Through
[the common tasks](../tasks/) a person needs neither: `go tool task versions` and
`go tool task bump` take `gh`'s token when no token is set and `gh` is logged in, as `release`
does.

### An entry check could not read

An upstream that does not answer is not an entry that is up to date. A private repository asked
without a token answers 404, a registry can be down, and a version can be one its track cannot
order. `check` marks such a row `not read:` with what the upstream said, counts it apart, and
exits 2 rather than 1:

```
NAME   PINNED        NEWEST
spin   v20261114.01                not read: versions: https://api.github.com/repos/spin-stack/spin/releases/latest answered 404 Not Found
brand  main          main          behind main is now c6d8cb1f...
versions: 1 not read, and 1 behind of the 1 that were: a row not read may be behind, and says why (GITHUB_TOKEN or GH_TOKEN reads a private repository, and more than sixty requests an hour)
```

Exit 1 is "something is behind, and every entry was read"; exit 2 is "what is behind is not all
known". Exit 0 is never a file `check` was not shown whole. A `kernel` whose series kernel.org no
longer lists was read - the answer is that the pin stays - and so was an entry that `follows`
another.

## A refresh, on a schedule

`refresh` is the bump a workflow runs: each entry named moved to its newest, one commit each, and
with `-propose`, those commits pushed and opened as one pull request.

```yaml title="Taskfile.yml"
refresh:
  desc: Propose what is behind of the entries a build checks, as one pull request.
  cmds:
    - versions refresh -propose go kernel wolfi-base grype
```

```yaml title=".github/workflows/refresh.yml"
on:
  schedule:
    - cron: "17 9 * * 1"
  workflow_dispatch:
jobs:
  refresh:
    runs-on: ubuntu-latest
    permissions:
      contents: write
      pull-requests: write
    steps:
      - uses: actions/checkout@<commit> # v7.0.1
      - uses: actions/setup-go@<commit> # v7.0.0
        with:
          go-version-file: go.mod
      # A runner has no git identity, and refresh commits each entry it moves.
      - env:
          GITHUB_TOKEN: ${{ github.token }}
          GIT_AUTHOR_NAME: github-actions[bot]
          GIT_AUTHOR_EMAIL: 41898282+github-actions[bot]@users.noreply.github.com
          GIT_COMMITTER_NAME: github-actions[bot]
          GIT_COMMITTER_EMAIL: 41898282+github-actions[bot]@users.noreply.github.com
        run: go tool task refresh
```

The branch is `refresh/<label>-<the file's sum>`, so a set of versions is proposed once: open,
merged or closed by a person who said no to it, it is not asked again. A new set closes the open
pull request of the same `-label` it supersedes, with a comment that says which. An entry that
cannot be moved is listed in the body and fails the run, after the others are proposed. Each
entry's `note` is in the body, as what to check before merging.

A bump to another major version - the first number of the version, `v13.3.0` to `v14.0.0` - is
not committed: the body lists it as a person's, who moves it with `versions bump <name>`, and the
run does not fail for it. A schedule would otherwise take a new major the day it is tagged. A
version whose first number is a year, such as a snapshot's date, has no major to cross.

A pull request opened with a workflow's own `GITHUB_TOKEN` starts no workflow: a repository whose
checks must run on it opens it as a GitHub App, and commits as one. The token is `GH_TOKEN`'s when
it is set and `GITHUB_TOKEN`'s otherwise, as `gh` reads them, so the app's goes in `GH_TOKEN`. The
pull request's link is printed, and added to the run's summary.

## The gate

A file only stays the single source of truth if something enforces it. Add this test:

```go
func TestVersionsYAMLIsTheOnlyPin(t *testing.T) {
	g := gate.Gate{Root: "..", Elsewhere: []string{"go.sum"}}
	if err := g.Check(); err != nil {
		t.Error(err)
	}
}
```

It reads every file git lists, tracked or untracked and not ignored, and fails on:

- a sha256 or a commit written outside `versions.yaml`;
- a Dockerfile `ARG` that is a pin (`_IMAGE`, `_VERSION`, `_COMMIT`, `_SHA256`, `_SNAPSHOT`) with a
  default, or with no entry to supply it;
- an entry nothing reads;
- an entry a bump could not rewrite in place;
- an entry tracked `with` another that is not at its version;
- an entry one of [the common tasks](../tasks/) reads - `actionlint`, for `lint:workflows` - that a
  repository whose Taskfile includes them does not have.

Excused: the files `Elsewhere` names, tests, `_output/`, dot-directories other than `.github`, and
GitHub Actions pinned by commit (Dependabot moves those, as `go.sum` is Go's). An entry is read
when some file runs `versions args|env|version|ref <name>...` or `{{.VERSIONS}} ...`, a test
included; `Gate.Reads` changes what counts. Two reads are in no file of the repository, and the
gate counts them from what it knows of go-tools: `task`, which `go tool task` reads when `go.mod`
takes it, and what the common tasks read when a Taskfile includes `'{{.GO_TOOLS_TASKS}}'` - found
in those tasks' own file, at the go-tools version `go.mod` requires. The include has to be one: a
comment that names the variable reads nothing.

## As a library

`versions` reads the file (`Load`, or `Parse` for a program that embeds it). Reading runs nothing
and reaches nothing over the network, so a program can ship its pins and read them at run time,
as spin's binaries do. `versions/upstream` is the other half: where each entry stands against its
upstream, the bump, and `upstream.Pinned`, an entry's download that is accepted only if it is the
bytes the entry pins.
