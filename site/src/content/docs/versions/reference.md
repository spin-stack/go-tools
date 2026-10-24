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
| `archive` | for `git`: the sha256 of `git archive` at the commit, bumped by hand |
| `track` | how `check` and `bump` find a newer version |
| `note` | what a bump has to be checked with |

## Kinds

| kind | version | pin | build arguments |
|---|---|---|---|
| `image` | the tag | its digest | `NAME_IMAGE` (the reference by digest) |
| `git` | a tag or a branch | the commit; `archive:` the sha256 of `git archive` | `NAME_VERSION`, `NAME_COMMIT`, `NAME_ARCHIVE_SHA256` |
| `download` | put into `source`'s `{version}` (and `{major}`) | the file's sha256 | `NAME_VERSION`, `NAME_SHA256` |
| `date` | a point in time, such as the day a package archive is read as of | none | `NAME` |
| `module` | a Go program's version; `source` its package path, for `go install <source>@<version>` | none: the checksum database holds it | `NAME_VERSION` |
| `pypi` | the package's version; `source` its name | none: for a test's tool only | `NAME_VERSION` |

## Tracks

| track | the newest is |
|---|---|
| `github-release [repo]` | the repository's latest release (`source`'s by default), with a `v` only when the version has one |
| `tags <repo>` | the highest release-numbered tag of a git repository; for a download, the highest whose file is published |
| `commit <repo> <branch>` | the commit a branch is at, for a download whose version is a commit: behind only when the file changed |
| `go` | the newest stable Go, spelled as the tag is (`1.27.1-trixie`) |
| `kernel-stable` | the newest release of the same stable series; a series that has ended is for a person to decide |
| `digest` | the same tag: behind when it names another image |
| `branch` | the same branch: behind when it is at another commit |
| `pypi` | the package's newest on PyPI |
| `page <url>` | the first file the page links to named as `source`'s, with `{version}` the version |
| `today` | today's date |
| `follows <name>` | another entry's: `check` reports it for a person to read |

The format only grows. Every repository that uses go-tools reads the same file format, so a field,
a kind or a track is added, never renamed.
