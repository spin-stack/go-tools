# AGENTS.md - go-tools

The Go the spin-stack repositories build with (`README.md` says what each is). spin-ami and
spin-machine take it by the version their go.mod pins, and spin will.

- **A change here is a new version for every repository that uses it**, taken by each of them
  in its own commit (`go get github.com/spin-stack/go-tools@<commit>`), and checked there as that
  repository says. A change to the format of versions.yaml - a field, a kind, a track - is read
  by all of them: it adds, it does not rename.
- **Decisions are Go, and tested.** A track is a function with a test that needs no network.
- **Comments say why.** Not what the line does, and never what it used to be.
- **This repository pins with its own tool**: `versions.yaml`, held by `versions/gate/repo_test.go`.

| changed | run |
|---|---|
| Go | `task test`, `task lint` (spin's `.golangci.yml`, at spin's version) |
| a track or a kind | the above, and `go tool versions check` in a repository that has one |
