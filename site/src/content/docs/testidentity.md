---
title: testidentity
description: Fails on a commit of the branch whose author or committer is a test's - an address at a domain reserved for testing, or one the tree's tests spell - which is what a test that wrote git's author into the checkout leaves on every commit made from it.
---

A test that runs git looks for its repository upward from where it runs, and a test runs inside
the checkout. One that misses the repository it made - a mutant that lost its `-C`, a `TMPDIR`
inside the checkout, a hook's `GIT_DIR` - runs `git config user.email` on the checkout itself.
From then on every commit made there, from every worktree of it, is that test's: in spin it put
`Ana Lima <ana@corp.example>` on commits pushed to GitHub, twice, one of them onto main.

`testidentity` reads who each commit of the branch says wrote it and fails on one that is a
test's, before it is pushed and merged:

```sh
go tool testidentity [-base origin/main] [-allow file] [dir]
```

It checks every commit in `BASE..HEAD`, its author and its committer, as git wrote them - before a
`.mailmap` maps them to somebody.

## What a test identity is

Not a list kept by hand. An address is a test's when it is

- at a domain no registry delegates: `.test`, `.example`, `.invalid`, `.localhost` (RFC 2606 and
  6761), `.local` (RFC 6762), `.internal`, and `example.com`, `example.net`, `example.org`,
  `home.arpa` and their subdomains;
- at a domain with no dot at all: `t@t`, `dev@localhost`;
- spelled in one of the tree's tests or fixtures: a Go test (`_test.go`), a JavaScript or
  TypeScript one (`x.test.ts`, `x.spec.mjs`), a Terraform test (`.tftest.hcl`), an `.e2e.yaml`,
  or any file under a `testdata`, `fixtures`, `testutil`, `e2e`, `test`, `tests`, `__tests__` or
  `mocks` directory. The tree is read on every run, from `git ls-files`, so a fixture added
  tomorrow is refused tomorrow.

GitHub's own addresses are real by rule, before the tests are asked: an App's
`<id>+<name>[bot]@users.noreply.github.com` (Dependabot, a release App), a person's noreply, and
the `noreply@github.com` that commits what is merged on the site.

## What it says

Each finding is a line: the commit, its subject, which of author or committer, the address, and
why the address is a test's. After them, what to do, in the order that stops the next one first:
ask git where the author comes from (`git config --show-origin --get-all user.email`) in the
checkout the commit was made in and take out what the test wrote there; then give the branch's
commits their real author again, `git rebase --exec 'git commit --amend --no-edit
--reset-author' <base>`, and push with `--force-with-lease`. A commit already on the default
branch stays, and `.mailmap` maps its address.

The exit status is 1 on a finding, 2 when it could not ask - the base is not in the clone, which a
workflow's checkout needs `fetch-depth: 0` for.

## In a Taskfile

```yaml title="Taskfile.yml"
lint:
  cmds:
    - testidentity -base origin/main
```

Under `go tool task` the command is on Task's `PATH`; a repository with no `go.mod` runs
`go run github.com/spin-stack/go-tools/cmd/testidentity@<commit>`. On main the range is empty and
it passes.

## Exemptions

`-allow` names an address that reads as a test's and is somebody's real author, one a line,
each with its reason:

```text title="hack/testidentity-allow.txt"
ci@build.internal  # the build host's own commits of the generated tree, which are real
```

A line whose address is not a test identity any more fails too, so the list only shrinks. An
address a test spells is a test's from then on: a test that needs a person's address spells a
made-up one, never an author's.
