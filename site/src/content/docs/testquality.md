---
title: testquality
description: Fails on a test that cannot fail, or one skipped unconditionally. Such a test counts as covered, passes forever, and proves nothing.
---

A test that cannot fail is worse than no test: it shows up green, it counts toward coverage, and
the package looks tested. `testquality` finds two kinds:

- **Tests that cannot fail.** Nothing in them reaches `t.Error`, `t.Fatal`, `t.Skip`, an
  assertion package such as testify, or a helper that is handed the test.
- **Tests skipped unconditionally.** `t.Skip` as a top-level statement, so the test never runs.
  A skip inside an `if` is a decision about the environment and is fine.

```sh
go tool testquality ./...
```

## What it flags

```go
func TestOnlyLogs(t *testing.T) {
	// It says what somebody should check, and checks nothing.
	t.Log("the operator confirms the workspace came up")
}

func TestOnlyHelpersThatNeverSeeTheTest(t *testing.T) {
	if double(2) != 4 {
		_ = "nothing happens here either"
	}
}

func TestSkippedOutright(t *testing.T) {
	t.Skip("comes back when the feature is written")
	t.Fatal("never reached")
}
```

The first two are reported as `cannot fail` and the third as `is skipped unconditionally`. A test
that hands `t` to a helper counts as one that can fail, and so does one where a closure captures
`t` and calls an assertion package. Subtests are inspected with the rest of the body.

Tests behind the `integration` and `e2e` build tags are analyzed too: a test in another lane is
held like any other.

## Exceptions are a ratchet

Some findings are kept on purpose, for example a test that only checks that something compiles.
List them with `-allow`, one `<package>:<TestName>` a line, each with its reason:

```text title="hack/testquality-allow.txt"
github.com/spin-stack/spin/internal/api:TestTypesCompile  # the build is the assertion
```

```sh
go tool testquality -allow hack/testquality-allow.txt ./...
```

The key leaves out the line number so it survives edits above the test. An entry that matches
nothing fails too, so the list only shrinks: once a test is fixed, its exception has to go.
