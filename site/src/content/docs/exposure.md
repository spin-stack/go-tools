---
title: exposure
description: What a repository would show the world if it were made public - every ref's commits, pull request heads included, and its issues, comments and releases - read before anyone flips the switch.
---

Making a repository public publishes far more than its `main`. Every commit any ref reaches
becomes readable, and that includes the head of every pull request ever opened: GitHub keeps
them as `refs/pull/<n>/head`, publishes them with the repository, and only GitHub support can
remove them. So do the issues, the pull request bodies and their comments, the release notes and
assets, and every Actions log and artifact still kept.

`exposure` reads all of it before the switch:

```sh
go tool exposure -gitleaks-config hack/gitleaks.toml -allow hack/exposure-allow.txt spin-stack/spin
```

```text
exposure: spin-stack/spin: 6432 commit(s) in every ref, 2001 of them off the default branch; gitleaks v8.30.1 over every ref found 0 secret(s)
  private-ip 10.0.0.6: 9 place(s), first commit 0b57df9a5 internal/controlplane/.../ready_test.go, on the default branch
  private-repo-link github.com/spin-stack/ci: 1 place(s), first pull request #622, on the default branch
  ...
To look at, by a person:
  - 3113 file(s) some commit had and the default branch no longer does: deleted is not gone
  - 2446 Actions run(s): their logs turn public with the repository, and nothing here reads them - delete the ones nobody needs before the switch
  ...
```

Those 2001 commits off the default branch are the ones a secret scan of `main`'s history never
reads.

## What it reads

| what | how | a finding is |
|---|---|---|
| every commit of every ref, from a mirror clone (`git clone --mirror`, which brings `refs/pull/*`) | gitleaks, at the version go-tools pins, with the repository's own config | `secret:<rule>`, by gitleaks' fingerprint |
| every line any of those commits adds | the patterns below | `<kind> <value>` |
| every commit's author and committer | their addresses | `author-email` |
| issues, pull requests, their comments and review comments, release notes | the patterns below, through the GitHub API | `<kind> <value>` |

The patterns are for what is private without being a secret:

| kind | finds | passes over |
|---|---|---|
| `aws-account` | the account in an ARN, or after `account_id` | AWS's documented example accounts (`123456789012`, ...) |
| `private-ip` | 10/8, 172.16/12 and 192.168/16 addresses | public ones |
| `internal-host` | names under `.internal`, `.local`, `.corp`, `.lan`, `.intranet`, and EC2's `ip-a-b-c-d` | |
| `email` | a person's address | `example.*` and the test domains RFC 2606 and 6761 reserve, GitHub's no-reply addresses, `git@`, SSH extension names, systemd template instances |
| `private-repo-link` | a link into one of the owner's repositories that stays private | public repositories, and this one |

`-patterns <file>` adds a repository's own, one `<kind> <regexp>` a line: a customer's name, a
code name, a product nobody has announced.

Each finding says where it was first seen and whether the default branch reaches it. One that only
a pull request's head reaches is just as public, and cannot be removed by rewriting history.

## Answering a finding

A value is private, or it is public on purpose. Private: rotate it if it is a credential, rewrite
the history and ask GitHub support to drop the pull request refs that hold it, or keep the
repository private. Public on purpose: say so in the allowlist, one line a value with its reason:

```text title="hack/exposure-allow.txt"
private-ip 10.255.0.1  # the workspace's own gateway, which the network page documents
internal-host *  # *.spin.internal names are the product's, documented on the site
author-email ana@example.io  # the maintainer's public address
```

`<kind> *` answers every value of a kind. A line that answers nothing any more fails, as in the
other gates. A secret gitleaks reports by mistake is answered in the repository's gitleaks config
instead, where its own `lint:secrets` reads it too.

## What it lists for a person

Some things no pattern can judge, and `exposure` lists them rather than pretending to:

- **Deleted files.** A file some commit had and `main` no longer has is still in the history:
  that is where accidents usually are.
- **Binary files.** An image or an archive is read by opening it.
- **Issues and pull requests.** The patterns read them; a person reads what no pattern names.
- **Actions runs and artifacts.** Their logs and files turn public with the repository and are not
  read here. Delete the ones nobody needs before the switch.
- **Release assets.** Their notes are read; the assets are not.

## Running it

```sh
go get -tool github.com/spin-stack/go-tools/cmd/exposure
go tool exposure [-allow file] [-patterns file] [-gitleaks-config path] [-mirror dir] [-private repos] <owner/repo>
```

| flag | |
|---|---|
| `-allow` | values public on purpose, with why |
| `-patterns` | the repository's own kinds |
| `-gitleaks-config` | the repository's gitleaks config, as a path in its tree at `HEAD` |
| `-mirror` | keep the mirror clone in a directory, fetched on the next run; a temporary one by default |
| `-private` | the owner's repositories that stay private, comma-separated; by default those the token can see. A workflow's token sees none, so a scheduled job names them |
| `-github=false` | skip the GitHub API: commits only |

It needs `git`, `go` (gitleaks runs as `go run` at the pinned version) and, for a private
repository, a GitHub token: `GITHUB_TOKEN`, or `gh auth token`. It exits 0 when every finding is
answered, 1 when one is not, and 2 when it could not read the repository. The website takes seconds,
spin's 6432 commits about 35.

It is a check for the day before a repository goes public, and for a repository that already is,
from a scheduled job: a value that lands in a pull request after the switch is published the
moment the pull request is opened.
