# go-tools

[![CI](https://github.com/spin-stack/go-tools/actions/workflows/ci.yml/badge.svg)](https://github.com/spin-stack/go-tools/actions/workflows/ci.yml)

Checks for what a green build doesn't prove. Small Go tools the spin-stack repositories run from
their `go.mod`; each fails CI with a specific finding and says how to resolve it.

**Documentation: <https://spin-stack.github.io/go-tools/>**

| tool | what it catches |
|---|---|
| [`mutate`](https://spin-stack.github.io/go-tools/mutate/) | code your change touched that no test checks, even at 100% coverage: it breaks the code one small edit at a time and fails when the tests still pass |
| [`testquality`](https://spin-stack.github.io/go-tools/testquality/) | tests that cannot fail, and tests skipped unconditionally |
| [`ctxlife`](https://spin-stack.github.io/go-tools/ctxlife/) | goroutines that use their caller's context and outlive the call |
| [`refs`](https://spin-stack.github.io/go-tools/refs/) | paths, tasks and packages that AGENTS.md or CLAUDE.md names and the tree no longer has |
| [`vulngate`](https://spin-stack.github.io/go-tools/vulngate/) | vulnerabilities the code calls and that have a fix |
| [`versions`](https://spin-stack.github.io/go-tools/versions/) | pins written outside `versions.yaml`, and pins behind their upstream |
| [`sfw`](https://spin-stack.github.io/go-tools/sfw/) | an npm, pnpm or yarn install that does not go through Socket Firewall, and a workflow that runs a package manager instead of a task |
| [`provenance`, `sbom`, `attestgate`, `trustroot`](https://spin-stack.github.io/go-tools/supply-chain/) | releases published without SLSA provenance or an SBOM |

```sh
go get -tool github.com/spin-stack/go-tools/cmd/mutate
go tool mutate -base origin/main
```

`versions`, `versions/upstream`, `versions/gate`, `provenance`, `attestgate`, `sbom` and
`trustroot` are also packages another repository imports; the rest is `internal/`.

## Working here

```
task test      # the Go tests, under the race detector, held to a coverage floor
task lint      # golangci-lint with spin's rules, and this repository's own gates run on itself
task mutate    # break what the change against BASE (origin/main) touched, and ask the tests
task site      # the documentation site (site/), built
```

The site is Astro Starlight under `site/`, styled as [spin-stack/website](https://github.com/spin-stack/website),
and published to GitHub Pages from `main`. The example on its mutate page is
`cmd/mutate/testdata/example`, which `TestTheExampleTheSiteShows` runs on every CI run.

## License

Apache License 2.0. See [LICENSE](LICENSE).
