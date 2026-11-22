---
title: consumers
description: Not a check - a report of which commit of a repository each place that copies from it is on, whether its copies are still that commit's, and whether the npm packages it installs are the ones pinned there.
---

Some repositories make files others copy: brand's stylesheets and marks are committed into the
dashboard, the website and the blog. A copy says nothing about where it came from, and a
repository that changes an output cannot see who still has the old one. `consumers.toml` names
every place that copies, and `consumers` reads each one and says where it is.

```toml title="consumers.toml"
name = "brand"            # what a consumer's versions.yaml calls this repository
scope = "@fontsource"     # optional: the npm scope whose packages are this repository's to pin

[[consumer]]
name = "website"          # what the report calls it
repo = "spin-stack/website"
pin = "versions.yaml"     # the file, there, whose `brand` entry names the commit it is on
packages = "package.json" # optional: held to the npm packages versions.yaml pins here
[consumer.copies]
"src/styles/brand-tokens.css" = "dist/tokens.css"   # its file = the file here it is a copy of
```

```sh
go tool task consumers
go tool task consumers -- -checkout spin-stack/spin=../spin -checkout spin-stack/t3code=../t3code@fork/spin
```

For each consumer, read at its default branch through `gh api`, or from a checkout given with
`-checkout owner/name=dir[@ref]`:

- **pin** - the commit its `versions.yaml` pins, the release that names it, the newest release,
  and how many commits behind main it is. A consumer with no pin is compared with main.
- **copies** - whether each copied file is byte for byte what this repository had at that commit.
- **packages** - whether its `package.json` installs the npm packages this repository's
  `versions.yaml` pinned at that commit, at those versions, and nothing else of `scope`. For
  brand these are the faces its stylesheets name, which a consumer installs and does not copy.

It exits 0 whatever it finds. A consumer that is behind is that repository's to fix, and the
gate of the one it copies from must not fail for it. A repository that cannot be read is said
so, and the others are still reported. The clone it runs in has to hold the commits the
consumers pin, which a shallow clone does not.

A field the table does not know is refused, so a misspelt `packages` is not a row held to less.
A repository's own test can read the table with the `consumers` package's `Load`, to hold it to
the tree: that every file a row copies is one it has.
