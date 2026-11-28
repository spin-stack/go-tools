# AGENTS.md - go-tools

The Go the spin-stack repositories build with (`README.md` says what each is).

- **A command's release is in every repository's next run**, with no pull request there: each
  runs go-tools' latest release through `task` (`cmd/task`, installed by the root `action.yml` or
  `go install`, in no go.mod). So a release that adds a finding fails every repository that has
  it at once: a finding a repository could not have answered yet lands after each has. A package
  another repository imports - `versions`, `provenance` - is the exception, taken by its go.mod
  in Dependabot's week. A change to the format of versions.yaml - a field, a kind, a track - is
  read by all of them: it adds, it does not rename.
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
