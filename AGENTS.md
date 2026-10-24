# AGENTS.md - go-tools

The Go the spin-stack repositories build with (`README.md` says what each is). spin-ami and
spin-machine take it by the version their go.mod pins, and spin will.

- **A change here is a new version for every repository that uses it**, taken by each of them
  in its own commit (`go get github.com/spin-stack/go-tools@<commit>`), and checked there as that
  repository says. A change to the format of versions.yaml - a field, a kind, a track - is read
  by all of them: it adds, it does not rename.
- **Decisions are Go, and tested.** A track is a function with a test that needs no network.
- **Comments say why.** Not what the line does, and never what it used to be.
- **What a tool does for a reader is on the site** (`site/`, Astro Starlight, published to
  https://spin-stack.github.io/go-tools/): a page per tool, in plain words, with an example. A
  change to what a tool catches, a flag, or a marker such as `mutate-exempt` changes its page in
  the same commit. Output a page shows is a file a test writes, never retyped.
- **This repository pins with its own tool**: `versions.yaml`, held by `versions/gate/repo_test.go`.

| changed | run |
|---|---|
| Go | `task test`, `task lint` (spin's `.golangci.yml`, at spin's version) |
| a track or a kind | the above, and `go tool versions check` in a repository that has one |
| what mutate prints | `UPDATE_GOLDEN=1 go test ./cmd/mutate/`, and look at the site's mutate page |
| `site/` | `task site` |
