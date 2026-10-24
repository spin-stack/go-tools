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
  track: kernel-stable
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
go tool versions version kernel       # 7.2.8
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

## Keeping up

```sh
go tool versions check                # what is behind; exits 1 when anything is
go tool versions bump kernel          # to the newest the track finds
go tool versions bump kernel 7.2.9    # or to a version you name
```

A bump downloads what the new version names and pins its hash; for an image, it asks the registry
for the digest. It builds nothing, and the entry's `note` says what to run before committing.

`GITHUB_TOKEN`, when set, is sent to api.github.com, which answers only sixty unauthenticated
requests an hour.

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
- an entry a bump could not rewrite in place.

Excused: the files `Elsewhere` names, tests, `_output/`, dot-directories other than `.github`, and
GitHub Actions pinned by commit (Dependabot moves those, as `go.sum` is Go's). An entry is read
when some file runs `versions args|env|version|ref <name>...` or `{{.VERSIONS}} ...`, a test
included; `Gate.Reads` changes what counts.

## As a library

`versions` reads the file (`Load`, or `Parse` for a program that embeds it). Reading runs nothing
and reaches nothing over the network, so a program can ship its pins and read them at run time,
as spin's binaries do. `versions/upstream` is the other half: where each entry stands against its
upstream, the bump, and `upstream.Pinned`, an entry's download that is accepted only if it is the
bytes the entry pins.
