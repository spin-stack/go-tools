package gotest_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/ghatrace"
	"github.com/spin-stack/go-tools/internal/gotest"
)

var t0 = time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC)

// ev is one line of go test -json, s seconds into the run.
func ev(s float64, action, pkg, test, output string) string {
	at := t0.Add(time.Duration(s * float64(time.Second))).Format(time.RFC3339Nano)
	return fmt.Sprintf(`{"Time":%q,"Action":%q,"Package":%q,"Test":%q,"Output":%q}`, at, action, pkg, test, output)
}

const pkg = "github.com/o/r/internal/e2e"

// A run of one package: a slow test with a fast and a slow subtest, a fast test, a failing test,
// and a package with no tests - with lines from a wrapper in front of go test, one of them JSON
// that is no event.
var run = []string{
	"starting the database",
	`{"level":"info","msg":"the database is up"}`,
	ev(0, "start", pkg, "", ""),
	ev(0.1, "run", pkg, "TestSlow", ""),
	ev(0.1, "output", pkg, "TestSlow", "=== RUN   TestSlow\n"),
	ev(0.2, "run", pkg, "TestSlow/fast", ""),
	ev(0.3, "pass", pkg, "TestSlow/fast", ""),
	ev(0.3, "run", pkg, "TestSlow/slow", ""),
	ev(5.3, "output", pkg, "TestSlow/slow", "    slow_test.go:9: booted\n"),
	ev(5.3, "pass", pkg, "TestSlow/slow", ""),
	ev(5.4, "output", pkg, "TestSlow", "--- PASS: TestSlow (5.30s)\n"),
	ev(5.4, "pass", pkg, "TestSlow", ""),
	ev(5.4, "run", pkg, "TestFast", ""),
	ev(5.5, "pass", pkg, "TestFast", ""),
	ev(5.5, "run", pkg, "TestBroken", ""),
	ev(5.5, "output", pkg, "TestBroken", "=== RUN   TestBroken\n"),
	ev(7.5, "output", pkg, "TestBroken", "    broken_test.go:3: no host\n"),
	ev(7.5, "output", pkg, "TestBroken", "--- FAIL: TestBroken (2.00s)\n"),
	ev(7.5, "fail", pkg, "TestBroken", ""),
	ev(7.6, "output", pkg, "", "FAIL\n"),
	ev(7.6, "output", pkg, "", "FAIL\tgithub.com/o/r/internal/e2e\t7.6s\n"),
	ev(7.6, "fail", pkg, "", ""),
	ev(7.7, "start", "github.com/o/r/cmd", "", ""),
	ev(7.7, "output", "github.com/o/r/cmd", "", "?   \tgithub.com/o/r/cmd\t[no test files]\n"),
	ev(7.7, "skip", "github.com/o/r/cmd", "", ""),
}

func read(t *testing.T, verbose bool) (string, []string) {
	t.Helper()
	var out strings.Builder
	var spans []string
	r := &gotest.Reader{Out: &out, Verbose: verbose, Min: time.Second, Parent: "go test", Record: func(s ghatrace.Span) {
		spans = append(spans, fmt.Sprintf("%s under %s, %s, exit %d", s.Name, s.Parent, s.End.Sub(s.Start), s.Exit))
	}}
	for _, line := range run {
		require.NoError(t, r.Line([]byte(line)))
	}
	return out.String(), spans
}

func TestEachPackageAndEachLongTestIsASpanUnderWhatHoldsIt(t *testing.T) {
	_, spans := read(t, false)
	assert.Equal(t, []string{
		"TestSlow/slow under TestSlow, 5s, exit 0",
		"TestSlow under internal/e2e, 5.3s, exit 0",
		"TestBroken under internal/e2e, 2s, exit 1",
		"internal/e2e under go test, 7.6s, exit 1",
	}, spans, "a test under a second is no span, a package with no tests neither")
}

// packageFailed is how go test ends a package that failed: the test binary's verdict, then its own.
const packageFailed = "FAIL\n" + "FAIL\tgithub.com/o/r/internal/e2e\t7.6s\n"

func TestWhatGoTestWouldHaveWritten(t *testing.T) {
	for _, c := range []struct {
		name    string
		verbose bool
		want    string
	}{
		{"without -v: a test says something only when it fails, and no === line", false,
			"starting the database\n" + `{"level":"info","msg":"the database is up"}` + "\n" +
				"    broken_test.go:3: no host\n--- FAIL: TestBroken (2.00s)\n" +
				packageFailed +
				"?   \tgithub.com/o/r/cmd\t[no test files]\n"},
		{"with -v: every line", true,
			"starting the database\n" + `{"level":"info","msg":"the database is up"}` + "\n" +
				"=== RUN   TestSlow\n    slow_test.go:9: booted\n--- PASS: TestSlow (5.30s)\n" +
				"=== RUN   TestBroken\n    broken_test.go:3: no host\n--- FAIL: TestBroken (2.00s)\n" +
				packageFailed +
				"?   \tgithub.com/o/r/cmd\t[no test files]\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, _ := read(t, c.verbose)
			assert.Equal(t, c.want, out)
		})
	}
}

// Without -v a passing package is its "ok" line: the test binary's PASS before it is go test's own.
func TestAPassingPackageWithoutVIsItsOKLine(t *testing.T) {
	var out strings.Builder
	r := &gotest.Reader{Out: &out}
	for _, line := range []string{
		ev(0, "start", pkg, "", ""),
		ev(0, "run", pkg, "TestA", ""),
		ev(0, "output", pkg, "TestA", "=== RUN   TestA\n"),
		ev(0, "output", pkg, "TestA", "--- PASS: TestA (0.00s)\n"),
		ev(0, "pass", pkg, "TestA", ""),
		ev(0, "output", pkg, "", "PASS\n"),
		ev(0, "output", pkg, "", "ok  \tgithub.com/o/r/internal/e2e\t0.1s\n"),
		ev(0, "pass", pkg, "", ""),
	} {
		require.NoError(t, r.Line([]byte(line)))
	}
	assert.Equal(t, "ok  \tgithub.com/o/r/internal/e2e\t0.1s\n", out.String())
}

func TestShort(t *testing.T) {
	for in, want := range map[string]string{
		"github.com/o/r/internal/e2e": "internal/e2e",
		"github.com/o/r":              "github.com/o/r",
		"example/internal/x":          "example/internal/x",
	} {
		assert.Equal(t, want, gotest.Short(in))
	}
}
