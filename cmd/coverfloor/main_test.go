package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// probe.go's lines: tested is 6-8, boots (lane) 12-15 with a closure inside, plain 18-20, and
// undocumented 25.
const probe = `package probe

// tested is run by CI's tests.
//
// Its comment is two paragraphs long, so its lines are as written.
func tested() int {
	return 1
}

// boots starts a machine.
// mutate-lane: TestBoots
func boots() {
	f := func() {}
	f()
}

// plain is not lane code, and nothing ran it.
func plain() {
	_ = 0
}

var declared = 1

func undocumented() { _ = declared }
`

// module is a module holding probe.go, under source when that is not empty, the lane's test and
// a profile.
func module(t *testing.T, profile string, source ...string) (string, string) {
	t.Helper()
	body := probe
	if len(source) > 0 {
		body = source[0]
	}
	dir := t.TempDir()
	write := func(name, body string) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n\ngo 1.27\n")
	write("probe/probe.go", body)
	write("probe/probe_test.go", "package probe\n\nimport \"testing\"\n\nfunc TestBoots(t *testing.T) {}\n")
	write("coverage.out", profile)
	return filepath.Join(dir, "coverage.out"), dir
}

func TestLaneFunctionsAreLeftOutOfTheTotal(t *testing.T) {
	path, dir := module(t, `mode: atomic
example.com/m/probe/probe.go:6.20,8.2 1 3
example.com/m/probe/probe.go:12.13,13.15 1 1
example.com/m/probe/probe.go:13.15,13.16 0 0
example.com/m/probe/probe.go:14.2,14.5 1 0
example.com/m/probe/probe.go:18.14,20.2 1 0
example.com/m/probe/probe.go:25.21,25.36 1 0
`)
	got, err := measure(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := total{statements: 5, covered: 2, laneStatements: 2, laneCovered: 1}
	if got != want {
		t.Fatalf("measure = %+v, want %+v", got, want)
	}
	if p := 3 * got.percent(false); p != 100 {
		t.Errorf("without the lane: %v%%, want 1 of 3", p/3)
	}
	if p := got.percent(true); p != 40 {
		t.Errorf("with the lane: %v%%, want 40", p)
	}
	if p := (total{statements: 2, laneStatements: 2}).percent(false); p != 0 {
		t.Errorf("nothing but lane code: %v%%, want 0", p)
	}
}

// Two test binaries that both compiled a file each report its blocks, and one that ran a block
// covers it, as go tool cover merges them.
func TestABlockRunByEitherBinaryIsCovered(t *testing.T) {
	path, dir := module(t, `mode: atomic
example.com/m/probe/probe.go:18.14,20.2 1 0
example.com/m/probe/probe.go:18.14,20.2 1 1
example.com/m/probe/probe.go:6.20,8.2 1 0
`)
	got, err := measure(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := (total{statements: 2, covered: 1}); got != want {
		t.Fatalf("measure = %+v, want %+v", got, want)
	}
}

// The verdict is on the exact share: 2 of 3 is 66.67%, which -func would print as 66.7.
func TestTheFloorIsComparedUnrounded(t *testing.T) {
	path, dir := module(t, `mode: atomic
example.com/m/probe/probe.go:6.20,8.2 1 1
example.com/m/probe/probe.go:12.13,13.15 1 0
example.com/m/probe/probe.go:18.14,20.2 1 1
example.com/m/probe/probe.go:25.21,25.36 1 0
`)
	for _, c := range []struct {
		floor string
		code  int
		says  string
	}{
		{"66.7", 1, "coverage is 66.67% of what CI runs, below the floor of 66.7%\n"},
		{"66.66", 0, "coverage: 66.67% of what CI runs (floor 66.66%); 50.00% with the 1 statements only a lane runs\n"},
	} {
		var stdout, stderr strings.Builder
		if code := run([]string{c.floor, path}, dir, &stdout, &stderr); code != c.code || stdout.String() != c.says {
			t.Errorf("floor %s: exit %d saying %q (stderr %q), want %d saying %q", c.floor, code, stdout.String(), stderr.String(), c.code, c.says)
		}
	}
}

func TestWhatTheGateCannotReadExitsTwo(t *testing.T) {
	path, dir := module(t, "mode: atomic\n")
	for _, args := range [][]string{{"1", path}, {"1"}, {"x", path}, {"1", path, path}} {
		var stdout, stderr strings.Builder
		if code := run(args, dir, &stdout, &stderr); code != 2 || stdout.Len() != 0 || stderr.Len() == 0 {
			t.Errorf("%q: exit %d, stdout %q, stderr %q; want 2 and only stderr", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestWhatIsNotAProfileIsRefused(t *testing.T) {
	for name, profile := range map[string]string{
		"no mode":        "example.com/m/probe/probe.go:6.20,8.2 1 3\n",
		"no blocks":      "mode: atomic\n",
		"a short line":   "mode: atomic\nexample.com/m/probe/probe.go:6.20,8.2 1\n",
		"no range":       "mode: atomic\nexample.com/m/probe/probe.go 1 3\n",
		"no end":         "mode: atomic\nexample.com/m/probe/probe.go:6.20 1 3\n",
		"a bad count":    "mode: atomic\nexample.com/m/probe/probe.go:6.20,8.2 1 x\n",
		"another module": "mode: atomic\nexample.com/other/probe.go:6.20,8.2 1 3\n",
		"a missing file": "mode: atomic\nexample.com/m/gone.go:6.20,8.2 1 3\n",
	} {
		t.Run(name, func(t *testing.T) {
			path, dir := module(t, profile)
			if got, err := measure(path, dir); err == nil {
				t.Fatalf("measure = %+v, want an error", got)
			} else if !strings.Contains(err.Error(), path) && !strings.Contains(err.Error(), "gone.go") {
				t.Errorf("error %q names neither the profile nor the file", err)
			}
		})
	}
}

// A module with no lane code says only its total: there is no second number to give.
func TestWithoutLaneCodeOneNumberIsGiven(t *testing.T) {
	path, dir := module(t, `mode: atomic
example.com/m/probe/probe.go:6.20,8.2 1 1
example.com/m/probe/probe.go:18.14,20.2 1 0
`)
	var stdout, stderr strings.Builder
	if code := run([]string{"50", path}, dir, &stdout, &stderr); code != 0 || stdout.String() != "coverage: 50.00% of what CI runs (floor 50%)\n" {
		t.Errorf("exit %d saying %q (stderr %q)", code, stdout.String(), stderr.String())
	}
}

// The marker is mutate's, read by mutate's code: one that names no test, or a test that is not
// there, leaves nothing out and is refused, as mutate refuses it.
func TestALaneNamingNoTestIsRefused(t *testing.T) {
	for name, marker := range map[string]string{
		"no test":          "mutate-lane:",
		"a test not there": "mutate-lane: TestGone",
	} {
		t.Run(name, func(t *testing.T) {
			path, dir := module(t, "mode: atomic\nexample.com/m/probe/probe.go:4.13,4.14 0 0\n",
				"package probe\n\n// "+marker+"\nfunc boots() {}\n")
			var stdout, stderr strings.Builder
			if code := run([]string{"1", path}, dir, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "boots") {
				t.Errorf("exit %d, stderr %q; want 2 naming boots", code, stderr.String())
			}
		})
	}
}
