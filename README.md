# go-tools

The Go the spin-stack repositories build with. Nothing here is linked into what any of them
ships.

## versions

`versions.yaml` at a repository's root is every input it pins from outside it - a base image, an
upstream tarball, a repository's commit, the day a package archive is read as of - written once.
The package `versions` reads it, and a bump rewrites one entry's two lines and nothing else.
`cmd/versions` is the command a Taskfile runs; `versions.Gate`, run from a test, is what fails
when a pin is written anywhere else.

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

The gate fails on a sha256 or a commit written outside versions.yaml (but in the files
`Elsewhere` names, tests, `_output/`, dot-directories but `.github`, and an action a workflow
runs by its commit), on a Dockerfile `ARG` that is a pin with a default or with no entry to hand
it, and on an entry nothing reads. What reads an entry is `versions args|env|version|ref <name>...`
or `{{.VERSIONS}} ...`; `Gate.Reads` says otherwise.

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
