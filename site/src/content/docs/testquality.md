---
title: testquality
description: Fails on a test that cannot fail, one skipped unconditionally, or one that sleeps a second or more where a condition should be waited for. The first two count as covered, pass forever and prove nothing; the third passes on a quiet machine and fails on a loaded one.
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

func TestWaitsOnTheClock(t *testing.T) {
	start(t)
	time.Sleep(5 * time.Second) // for the server to be up, presumably
	assertUp(t)
}
```

The first two are reported as `cannot fail`, the third as `is skipped unconditionally`, and the
fourth as `sleeps 5s for nothing it can see`. A test that hands `t` to a helper counts as one that
can fail, and so does one where a closure captures `t` and calls an assertion package. Subtests
are inspected with the rest of the body.

A sleep of a second or more in a test's body - a subtest's or a goroutine's included - is a wait
on the clock: it passes on a quiet machine and fails on a loaded one, which is what a flake is.
Of 45 failed fleet gates in two days of one repository, the real flakes were each a wait with a
bound that the machine's load outran. Wait for the condition - a channel, a log line, an
`Eventually` - or say in the allowlist why the clock is the only signal there is. A sleep inside a
loop looks again, which is a wait for a condition with the loop's own bound, and is not flagged;
nor is one under a second, which paces; nor a duration the test computes, which is not a number to
judge; a helper's sleep is the helper's, one line to fix however many tests call it.

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
