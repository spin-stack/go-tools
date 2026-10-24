---
title: vulngate
description: govulncheck as a CI gate. Fails only on a vulnerability the code calls and that has a fix, the standard library included.
---

`govulncheck` reports a lot: vulnerabilities in modules you import but never call, and ones with
no fix yet. A gate that fails on all of it gets ignored. `vulngate` fails on the findings you can
and should act on today:

| finding | result |
|---|---|
| the code calls the vulnerable function, and a fixed version exists | **fails**: upgrade |
| the module is imported, but the vulnerable code is never called | listed, passes |
| no fixed version exists yet | listed, passes |

```sh
go tool vulngate ./...
go tool vulngate -tags=integration ./...   # govulncheck flags, each as -flag=value
```

For the standard library, the upgrade is the `toolchain` line in `go.mod`.

It asks vuln.go.dev for the database, so it needs the network.

## One process per program

`vulngate` scans each `main` package the patterns name in a process of its own, then what no `main`
imports. One call graph of everything took 11.5 GB on spin; its largest program alone took 4.1 GB.
