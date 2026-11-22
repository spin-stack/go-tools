---
title: coverfloor
description: Fails when the tests cover less than a floor of the code CI can run, leaving out the functions mutate-lane marks as held by a test CI cannot run, and comparing the exact share rather than a rounded one.
---

A coverage floor stops a change that adds code no test runs. `coverfloor` holds the profile
`go test -coverprofile` wrote to one:

```sh
go test -race -covermode=atomic -coverprofile=coverage.out ./...
coverfloor 80 coverage.out
```

It runs from the module's root. It exits 0 at or above the floor, 1 below it, and 2 when it cannot
read the profile, the floor, or a file the profile names. It prints the share it compared, and,
when the module has lane code, the share with that code counted too.

## Code only a lane runs

A function whose doc comment carries [`mutate-lane: <TestName>`](../mutate/#when-a-survivor-is-not-a-missing-test)
is held by a test that needs root, KVM or a cloud account, which a CI runner has none of. `mutate`
makes no edit in it, and `coverfloor` leaves its statements out of the share, reading the marker
with mutate's own code: a marker that names no test, or a test that is not there, fails both.

Counted, such code pulls the total down however well everything around it is tested. In
spin-machine every probe its lab gained lowered the number, and a floor that had to be lowered to
let one through stopped being a floor.

## Why not `go tool cover -func`

`go tool cover -func` rounds its total to one decimal. spin-machine's 79.96% read as 80.0 and
passed a floor of 80. `coverfloor` compares the exact share, and prints it to two decimals.
