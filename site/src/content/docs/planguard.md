---
title: planguard
description: Whether an OpenTofu plan may be applied without a person reading it - nothing, apply, or person - so an installation can apply on a schedule what only adds and updates, and leave the rest to a pull request.
---

An installation that follows a release channel, or its module's main, changes without anybody
writing a commit: a new release moves the images its launch templates name, a new module commit
moves whatever it moves. Applying each of those by hand is a plan read and a button pressed for a
change nobody chose. Applying all of them on a schedule is how a database gets replaced on a
Tuesday night. `planguard` draws the line between the two from the plan itself.

```sh
tofu plan -out=plan.bin
tofu show -json plan.bin > plan.json
planguard [-replaceable aws_launch_template,...] plan.json
```

| the plan | verdict |
|---|---|
| no resource changes - nothing, or only data sources read | `nothing` |
| creates and in-place updates, and nothing else | `apply`: applied unread |
| any delete, replacement (delete and create, either order), forget, or an action it does not know | `person`: each such change is listed |

The plan cannot say which resources hold data, so every resource is treated as one that might:
a replaced bucket, database or KMS key is a person's whatever its type. A type made to be replaced -
`-replaceable aws_launch_template` - may be replaced unread, but never deleted alone.

The verdict is the first line on stdout and the changes held follow it; in GitHub Actions it is
also `verdict=<it>` in `$GITHUB_OUTPUT`, for a step's `if:`. Every verdict exits 0 - a plan that is
a person's is not a failed run - and what cannot be read exits 2, never `nothing`.

```yaml title=".github/workflows/auto.yml"
- run: task plan:json      # tofu plan -out, tofu show -json
- id: guard
  run: task guard          # planguard plan.json
- if: steps.guard.outputs.verdict == 'apply'
  run: task apply:saved    # tofu apply plan.bin: what was judged is what is applied
```
