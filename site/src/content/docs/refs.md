---
title: refs
description: Fails when AGENTS.md or CLAUDE.md names a path, a task, an ADR or a package the tree no longer has, so the instructions an agent loads stay true.
---

The prose an agent loads into every session - `AGENTS.md`, `CLAUDE.md` - is full of references:
"tests live in `internal/store`", "run `task lint`", "see ADR-0007". When the tree moves on and the
prose doesn't, an agent follows directions to things that no longer exist. `refs` checks those
references against the tree:

| reference | resolves when |
|---|---|
| a repository path, `cmd/mutate/main.go` | the file or directory exists |
| `` `task <name>` `` | `task` lists that name, in the root or in the directory's own Taskfile |
| `ADR-0007` | a decision record with that number is in `docs/decisions` (`-decisions`) |
| `pkg.Symbol` of an `internal/` package | the package still exists |

```sh
go tool refs
```

It reads `AGENTS.md CLAUDE.md */AGENTS.md */CLAUDE.md` by default; `-files` names others.

## On purpose

Sometimes prose names something that isn't there by design: a path in another repository, or a
file mentioned to say it was removed. List those with `-allow`, one a line, as
`<file><TAB><reference>  # why`. As with testquality, an entry that matches nothing fails too, so an
exception can't outlive the sentence it excuses.

## Why it refuses an empty task list

If `task` lists nothing - a parse that read nothing, a Taskfile that failed to load - every
`task <name>` would look broken. `refs` treats an empty list from a repository that has a Taskfile
as an error, not as a list of findings. A report of "34 broken references" from a parser that read
nothing is a false alarm someone would act on.
