---
title: sfw
description: Every npm, pnpm and yarn install through Socket Firewall Free, at a pinned binary, and a gate that fails on one that is not - or on a workflow that runs a package manager itself.
---

An `npm install` runs code from strangers: a package's install scripts run the moment it lands,
before any review or test. A compromised release of a popular package is in thousands of
`node_modules` within hours. [Socket Firewall Free](https://docs.socket.dev/docs/socket-firewall-free)
stands between the package manager and the registry and refuses a package Socket has confirmed
as malware before it is downloaded. It needs no account.

`go tool sfw` makes that the only way a spin-stack repository installs packages:

```sh
go tool sfw pnpm install --frozen-lockfile   # the install, through the firewall
go tool sfw check                            # fails on an install that isn't
```

## Running an install through it

```sh
go get -tool github.com/spin-stack/go-tools/cmd/sfw
```

`go tool sfw <package manager> <args>...` runs the command through the firewall and exits with its
status. The firewall's binary is pinned by sha256 for each platform (linux and macOS, amd64 and
arm64) in [`cmd/sfw/versions.yaml`](https://github.com/spin-stack/go-tools/blob/main/cmd/sfw/versions.yaml),
which the command embeds. It is downloaded the first time into the user's cache directory and
checked against its pin on every run: Socket doesn't sign the binaries, so the pin is what makes
it the one that was reviewed. A repository gets a newer firewall by moving to a newer go-tools.

## The gate

`go tool sfw check`, from `task lint`, reads the files git lists and fails on two things:

| where | fails on | the fix |
|---|---|---|
| Taskfiles, shell scripts, Dockerfiles | a command that fetches packages without the firewall: `npm install`, `npm ci`, `pnpm install`, `pnpm add`, `pnpm dlx`, `yarn`, `npx`, ... | `go tool sfw <the command>` |
| workflows and composite actions | any package manager command, the firewall's included | a task that runs it, and `task <name>` in the workflow |

Commands that only run what is already installed - `pnpm build`, `pnpm run test`, `pnpm exec` -
are left alone outside workflows. Text that only names a command, like a task's `desc` or a
comment, is not a command.

The workflow rule is the other half: CI runs `task site`, not `pnpm install && pnpm build`, so
what CI does is what a developer's `task site` does, and the install in it goes through the
firewall in both places.

```yaml title="Taskfile.yml"
site:
  cmds:
    - go tool sfw pnpm --dir site install --frozen-lockfile
    - pnpm --dir site build
```

```yaml title=".github/workflows/pages.yml"
- uses: pnpm/action-setup@<commit> # v6.1.0
- uses: actions/setup-node@<commit> # v7.1.0
  with:
    node-version: '22'
- run: task site
```

## What it does not cover

- **A cache.** A package restored from pnpm's store or a CI cache never reaches the registry, so
  the firewall never sees it. Don't use `setup-node`'s `cache: pnpm` in a workflow that installs.
- **Suspected malware.** Free blocks what Socket has confirmed; what its AI only suspects is a
  warning, printed during the install.
- **Package managers themselves.** `pnpm/action-setup` and `corepack` fetch pnpm without the
  firewall. Pin the action by commit, and pin `packageManager` in `package.json`.
- **Offline.** The firewall asks Socket about each package, so an install needs the network.
