package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/internal/vulngate"
)

// A scan that did not run, or answered what is not govulncheck's json, is no answer: 2, never a
// clean 0. One that ran is judged; the flags given reach it after -format json's.
func TestAScanItCannotJudgeIsNotClean(t *testing.T) {
	answering := func(answer string, err error) scanner {
		return func(_ context.Context, args []string, stdout, _ io.Writer) error {
			if len(args) != 1 || args[0] != "m/cmd/app" {
				t.Errorf("the scan was given %v", args)
			}
			_, _ = io.WriteString(stdout, answer)
			return err
		}
	}
	for name, tc := range map[string]struct {
		scan   scanner
		status int
	}{
		"the scan failed":                    {scan: answering("", errors.New("no go.mod")), status: 2},
		"not json":                           {scan: answering("Scanning your code...", nil), status: 2},
		"clean":                              {scan: answering(`{"config":{}}`, nil), status: 0},
		"something the code calls has a fix": {scan: answering(`{"finding":{"osv":"GO-1","fixed_version":"v2","trace":[{"module":"m","package":"m/p","function":"F"}]}}`, nil), status: 1},
	} {
		t.Run(name, func(t *testing.T) {
			var out, errs bytes.Buffer
			assert.Equal(t, tc.status, run(t.Context(), tc.scan, oneProgram, []string{"./..."}, &out, &errs))
		})
	}
	// A go list that fails is no scan either.
	broken := func(context.Context, ...string) ([]string, error) { return nil, errors.New("no go.mod") }
	var out, errs bytes.Buffer
	assert.Equal(t, 2, run(t.Context(), answering(`{"config":{}}`, nil), broken, []string{"./..."}, &out, &errs))
}

// oneProgram is a module whose ./... is one main package and the library it imports.
func oneProgram(_ context.Context, args ...string) ([]string, error) {
	switch {
	case slices.Contains(args, "-deps"):
		return []string{"m/cmd/app", "m/lib", "fmt"}, nil
	case slices.ContainsFunc(args, func(a string) bool { return strings.Contains(a, `"main"`) }):
		return []string{"m/cmd/app"}, nil
	}
	return []string{"m/cmd/app", "m/lib"}, nil
}

// lister answers go list as a module would: named is what the patterns name, mains which of
// them are main packages, deps what those import, each as go prints it.
func lister(t *testing.T, named, mains, deps []string, want []string) golist {
	return func(_ context.Context, args ...string) ([]string, error) {
		if tags := slices.IndexFunc(args, func(a string) bool { return strings.HasPrefix(a, "-tags=") }); want != nil && (tags < 0 || args[tags] != want[0]) {
			t.Errorf("go list was asked %v, without %v", args, want)
		}
		switch {
		case slices.Contains(args, "-deps"):
			if !slices.Equal(args[len(args)-len(mains):], mains) {
				t.Errorf("deps asked of %v, want of %v", args, mains)
			}
			return deps, nil
		case slices.ContainsFunc(args, func(a string) bool { return strings.Contains(a, `"main"`) }):
			return mains, nil
		}
		return named, nil
	}
}

// Each main package is a run of its own, with the flags given, and what no main imports is one
// more; a module of libraries alone is one run of them, and no patterns is govulncheck's own.
func TestEachProgramIsScannedOnItsOwn(t *testing.T) {
	for name, tc := range map[string]struct {
		args               []string
		named, mains, deps []string
		tags               []string
		runs               [][]string
	}{
		"programs and what they import": {args: []string{"./..."},
			named: []string{"m/cmd/a", "m/cmd/b", "m/lib"}, mains: []string{"m/cmd/a", "m/cmd/b"}, deps: []string{"m/cmd/a", "m/cmd/b", "m/lib", "fmt"},
			runs: [][]string{{"m/cmd/a"}, {"m/cmd/b"}}},
		"a library no program imports": {args: []string{"-show=traces", "./..."},
			named: []string{"m/cmd/a", "m/lib", "m/hack/x"}, mains: []string{"m/cmd/a"}, deps: []string{"m/cmd/a", "m/lib"},
			runs: [][]string{{"-show=traces", "m/cmd/a"}, {"-show=traces", "m/hack/x"}}},
		"libraries alone": {args: []string{"./..."}, named: []string{"m/a", "m/b"},
			runs: [][]string{{"m/a", "m/b"}}},
		"build tags reach go list": {args: []string{"-tags=lane", "./..."}, tags: []string{"-tags=lane"},
			named: []string{"m/cmd/a"}, mains: []string{"m/cmd/a"}, deps: []string{"m/cmd/a"},
			runs: [][]string{{"-tags=lane", "m/cmd/a"}}},
		"no patterns": {args: []string{"-show=traces"}, runs: [][]string{{"-show=traces"}}},
	} {
		t.Run(name, func(t *testing.T) {
			runs, err := programs(t.Context(), lister(t, tc.named, tc.mains, tc.deps, tc.tags), tc.args)
			require.NoError(t, err)
			assert.Equal(t, tc.runs, runs)
		})
	}
	// go list failing at any of its three questions is no answer.
	for i := range 3 {
		calls := 0
		list := func(ctx context.Context, args ...string) ([]string, error) {
			calls++
			if calls == i+1 {
				return nil, errors.New("go list: exit status 1")
			}
			return oneProgram(ctx, args...)
		}
		_, err := programs(t.Context(), list, []string{"./..."})
		require.Error(t, err, "call %d", i+1)
	}
}

// Every run's findings are judged together: one run's called and fixed vulnerability fails the
// whole, whichever program it is in.
func TestFindingsOfEveryRunAreJudged(t *testing.T) {
	list := lister(t, []string{"m/cmd/a", "m/cmd/b"}, []string{"m/cmd/a", "m/cmd/b"}, []string{"m/cmd/a", "m/cmd/b"}, nil)
	var scanned []string
	scan := func(_ context.Context, args []string, stdout, _ io.Writer) error {
		scanned = append(scanned, args...)
		if args[0] == "m/cmd/b" {
			_, _ = io.WriteString(stdout, `{"finding":{"osv":"GO-1","fixed_version":"v2","trace":[{"module":"m","package":"m/p","function":"F"}]}}`)
		}
		return nil
	}
	var out, errs bytes.Buffer
	assert.Equal(t, 1, run(t.Context(), scan, list, []string{"./..."}, &out, &errs))
	assert.Equal(t, []string{"m/cmd/a", "m/cmd/b"}, scanned)
}

// TestMain is the scan's child when this test binary is run again as one, as vulngate is.
func TestMain(m *testing.M) {
	if os.Getenv(scanOne) != "" {
		os.Exit(child(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// A scan is this binary run again, as govulncheck: what govulncheck says reaches the caller, and
// a run it refuses is an error, not a clean answer. -version is one govulncheck refuses in json
// mode, in words only it says.
func TestEachScanIsAProcessOfItsOwn(t *testing.T) {
	const says = "-show flag is not supported for json output"
	var out, errs bytes.Buffer
	require.Error(t, ownProcess(t.Context(), []string{"-version"}, &out, &errs))
	assert.Contains(t, errs.String(), says)
	errs.Reset()
	assert.Equal(t, 2, child(t.Context(), []string{"-version"}, &out, &errs))
	assert.Contains(t, errs.String(), says)
}

// goList is go's own: the package this test is in is a main package.
func TestGoListAnswersAsGoDoes(t *testing.T) {
	mains, err := goList(t.Context(), "-f", `{{if eq .Name "main"}}{{.ImportPath}}{{end}}`, ".")
	require.NoError(t, err)
	assert.Equal(t, []string{"github.com/spin-stack/go-tools/cmd/vulngate"}, mains)
	_, err = goList(t.Context(), "./no-such-package")
	require.Error(t, err)
}

// The gate's exit status is 1 exactly when something the code calls has a fix, and it says what.
func TestTheExitStatusIsWhatFails(t *testing.T) {
	calledFixed := vulngate.Finding{OSV: "GO-1", FixedVersion: "v2", Trace: []vulngate.Frame{{Module: "m", Version: "v1", Package: "m/p", Function: "F"}}}
	onlyImported := vulngate.Finding{OSV: "GO-2", FixedVersion: "v2", Trace: []vulngate.Frame{{Module: "m", Version: "v1", Package: "m/q"}}}
	for name, tc := range map[string]struct {
		findings []vulngate.Finding
		status   int
		says     string
	}{
		"nothing":            {status: 0, says: "OK"},
		"only imported":      {findings: []vulngate.Finding{onlyImported}, status: 0, says: "not called, or not fixed: GO-2"},
		"called, with a fix": {findings: []vulngate.Finding{onlyImported, calledFixed}, status: 1, says: "called and fixed - upgrade: GO-1"},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			assert.Equal(t, tc.status, report(tc.findings, &out))
			assert.Contains(t, out.String(), tc.says)
		})
	}
}
