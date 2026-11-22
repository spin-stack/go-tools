---
title: versions.yaml reference
description: Every kind of entry versions.yaml takes, the build arguments each becomes, and every track that finds a newer version.
---

## An entry

| field | |
|---|---|
| `name` | how commands and build arguments refer to it: `kernel` becomes `KERNEL_*` |
| `kind` | what is pinned; see below |
| `source` | where it comes from: an image repository, a URL with `{version}`, a git repository, a Go package path |
| `version` | the version a person reads |
| `pin` | what makes it exact: a digest, a sha256, a commit |
| `pins` | for a `download` with one file per platform: the sha256 of each, by `os/arch` as Go names them, in place of `pin` |
| `archive` | for `git`: the sha256 of `git archive` at the commit, bumped by hand |
| `track` | how `check` and `bump` find a newer version |
| `bump` | the command that moves the entry, when that is not `versions bump`: see [An entry another command moves](#an-entry-another-command-moves) |
| `note` | what a bump has to be checked with |

## Kinds

| kind | version | pin | build arguments |
|---|---|---|---|
| `image` | the tag | its digest | `NAME_IMAGE` (the reference by digest) |
| `git` | a tag or a branch | the commit; `archive:` the sha256 of `git archive` | `NAME_VERSION`, `NAME_COMMIT`, `NAME_ARCHIVE_SHA256` |
| `download` | put into `source`'s `{version}` (and `{major}`) | the file's sha256 | `NAME_VERSION`, `NAME_SHA256` |
| `download` per platform | `source` also says `{os}` and `{arch}`, and `pins:` has a sha256 for each `os/arch` | each platform's sha256; `fetch` takes this machine's, and a bump hashes every one | `NAME_VERSION`, `NAME_SHA256_LINUX_AMD64`, ... |
| `date` | a point in time, such as the day a package archive is read as of | none | `NAME` |
| `module` | a Go program's version; `source` its package path, for `go install <source>@<version>` | none: the checksum database holds it | `NAME_VERSION` |
| `pypi` | the package's version; `source` its name | none: for a test's tool only | `NAME_VERSION` |

## Tracks

| track | the newest is |
|---|---|
| `github-release [repo] [prefix]` | the repository's latest release (`source`'s by default), with a `v` only when the version has one; with a prefix, a release is `<prefix><version>`; for an image, the same variant of it: `v2.15.0-alpine` moves to `v2.16.0-alpine` |
| `tags <repo>` | the highest release-numbered tag of a git repository; for a download, the highest whose file is published. A tag with anything after its numbers (`v11.2.0-rc0`) is no release. A download pinned at one is ordered by its numbers, before the release of the same numbers: `11.2.0-rc0` is behind once `11.2.0` is published, and not behind `11.1.2`. A pin that starts with no number is not ordered, and `check` says it could not read the entry |
| `commit <repo> <branch>` | the commit a branch is at, for a download whose version is a commit: behind only when the file changed |
| `go` | the newest stable Go, spelled as the tag is (`1.27.1-trixie`) |
| `kernel` | the newest release of the pinned series; moving to another series is naming it (`versions bump kernel 7.4.1`), and a series kernel.org no longer lists is reported and the pin stays |
| `digest` | the same tag: behind when it names another image |
| `branch` | the same branch: behind when it is at another commit |
| `pypi` | the package's newest on PyPI |
| `page <url>` | the first file the page links to named as `source`'s, with `{version}` the version |
| `today` | today's date |
| `with <name>` | `<name>`'s version, spelled with a `v` only when this entry's is: two files of one release, such as VictoriaMetrics and its vmutils. A bump of `<name>` moves this entry in the same file, and the gate fails while they differ |
| `with <name> pin` | `<name>`'s pin: a file named by another's commit, such as the VS Code server of a VS Code release |
| `with <owner/repo> <name> [pin]` | the same, of `<name>` in that GitHub repository's `versions.yaml` as its default branch has it now, read through the API (`GITHUB_TOKEN` or `GH_TOKEN` for a private one): `check` says when it moved, and a bump takes it. The gate reads no network, so it does not hold this one |
| `with <owner/repo>@release <name> [pin]` | the same, at the tag of that repository's latest release rather than its default branch: for an entry built with a release of the other, which would not match what it is built with were it moved to what main says first |
| `follows <name>` | another entry's, by a relation the file cannot compute, or another repository's: `check` reports it for a person to read |

The format only grows. Every repository that uses go-tools reads the same file format, so a field,
a kind or a track is added, never renamed.

## A download per platform

```yaml title="versions.yaml"
- name: task
  kind: download
  source: https://github.com/go-task/task/releases/download/v{version}/task_{os}_{arch}.tar.gz
  version: 3.54.0
  pins:
    linux/amd64: 680859dbb4d881a9c72d4d9a8f510825450849af8567deacd7302c01124416fb
    darwin/arm64: ...
  track: github-release https://github.com/go-task/task
```

A bump downloads every platform's file of the new version and rewrites each pin where it is
written; a release that lacks one of them is refused whole. The platforms are the ones the entry
lists: adding one is an edit, not a bump.

## An entry with another

```yaml title="versions.yaml"
- name: victoriametrics
  kind: download
  source: https://github.com/VictoriaMetrics/VictoriaMetrics/releases/download/{version}/victoria-metrics-linux-amd64-{version}.tar.gz
  version: v1.155.0
  pin: ...
  track: github-release https://github.com/VictoriaMetrics/VictoriaMetrics

- name: vmutils
  kind: download
  source: https://github.com/VictoriaMetrics/VictoriaMetrics/releases/download/{version}/vmutils-linux-amd64-{version}.tar.gz
  version: v1.155.0
  pin: ...
  track: with victoriametrics
```

`versions bump victoriametrics` pins both, and says each it moved. `versions bump vmutils` takes
victoriametrics' version and refuses any other. The entry named is one with a track of its own: a
chain of `with` is refused as the file is read, and so is one whose entry is not in it. An entry
with an `archive` is bumped by hand, so it refuses the bump of the entry it is with.

An entry can be with another repository's: spin-ami rebuilds the Grafana spin lays, at the tag spin
pins, so its entry is `with spin-stack/spin grafana`. When spin moves Grafana, spin-ami's
`versions check` says its own is behind, and `versions bump grafana` takes the version spin's file
has, spelled as spin-ami's entry spells its own.

## An entry another command moves

```yaml title="versions.yaml"
- name: spin
  kind: git
  source: https://github.com/spin-stack/spin
  version: v20261114.01
  pin: 51f7e0dea53e6f85aa855753766b688634487168
  track: github-release
  bump: go tool task sources:bump
```

Some entries move with rules the shared bump does not have. website's `sources:bump` waits for a
release to carry the screenshots the site needs and generates data again; spin-ami's
`spin-os upgrade` holds entries that move only with spin's release; spin-machine's
`spin-machine-dev bump` prints what a new machine type changes. `bump` is that command, one line,
as a person types it. Then:

- `versions bump spin`, `go tool task bump NAME=spin` and `versions refresh spin` refuse, and
  name it: ``spin is moved by `go tool task sources:bump`, as its entry says (bump:), and not by
  this command``. `-back` does not change that.
- `versions check` says it in the entry's row, `` moved by `go tool task sources:bump` ``, behind
  or not.
- The repository's command is not refused: it is built on `versions/upstream`'s `Bump`, which
  does not read the field. A command that shells out to `versions bump` for such an entry would
  be, and calls the library instead.

A `versions` from before the field refuses a file that has it (`field bump not found`), and
`go tool task` reads `versions.yaml` with the go-tools `go.mod` requires. So the field is added in
the commit that moves `go.mod` to a release that knows it, or after.
