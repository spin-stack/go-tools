package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spin-stack/go-tools/internal/linkweight"
)

// sizes are what moves with the Go toolchain and not with what the example says: a byte count,
// a package count.
var sizes = regexp.MustCompile(`\d+\.\d MB|\d+ packages`)

func normal(s string) string { return sizes.ReplaceAllString(s, "N") }

// The example the site shows is this run over testdata/example, and report.txt is what it printed:
// UPDATE_GOLDEN=1 writes it. The comparison leaves out the sizes, which a Go release moves.
func TestTheExampleTheSiteShows(t *testing.T) {
	if testing.Short() {
		t.Skip("links the example's two binaries")
	}
	t.Setenv("CGO_ENABLED", "0")
	t.Chdir(filepath.Join("testdata", "example"))
	report := filepath.Join(t.TempDir(), "report.json")
	var out, errs bytes.Buffer
	code := run([]string{"-top", "4", "-allow", "linkweight-allow.txt", "-json", report}, &out, &errs)
	if code != 0 {
		t.Fatalf("exit %d:\n%s%s", code, out.String(), errs.String())
	}
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile("report.txt", out.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("report.txt")
	if err != nil {
		t.Fatal(err)
	}
	if normal(out.String()) != normal(string(want)) {
		t.Errorf("the example printed what report.txt does not say (UPDATE_GOLDEN=1 rewrites it):\n%s", out.String())
	}
	// The JSON is the same report, for a comparison of two revisions.
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var bins []linkweight.Binary
	if err := json.Unmarshal(raw, &bins); err != nil {
		t.Fatal(err)
	}
	// Without a list it is a report, and a report is exit 0 whatever it says.
	if code := run([]string{"-roots", "./cmd/worker"}, &out, &errs); code != 0 {
		t.Errorf("a report without -allow: exit %d, %s", code, errs.String())
	}
	// A report it cannot write is no answer.
	if code := run([]string{"-json", filepath.Join(t.TempDir(), "none", "report.json")}, &out, &errs); code != 2 || !strings.Contains(errs.String(), "no such file") {
		t.Errorf("a -json it cannot write: exit %d, %s", code, errs.String())
	}
	if len(bins) != 2 || bins[0].Root != "cmd/api" || bins[0].MethodElimination || !bins[1].MethodElimination || bins[0].Shipped == 0 {
		t.Errorf("the JSON report = %+v", bins)
	}
}

// allowWithout is the example's list with the lines that contain drop removed and add added.
func allowWithout(t *testing.T, drop, add string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "example", "linkweight-allow.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for l := range strings.SplitSeq(string(raw), "\n") {
		if drop == "" || !strings.Contains(l, drop) {
			lines = append(lines, l)
		}
	}
	path := filepath.Join(t.TempDir(), "allow.txt")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+add), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A finding no line explains fails, and so does a line no finding matches; each is named.
func TestTheGateFailsBothWays(t *testing.T) {
	if testing.Short() {
		t.Skip("links the example's two binaries")
	}
	t.Setenv("CGO_ENABLED", "0")
	missing := allowWithout(t, "cmd/worker capability exec", "")
	stale := allowWithout(t, "", "cmd/worker capability mount  # it used to\n")
	t.Chdir(filepath.Join("testdata", "example"))
	for list, want := range map[string]string{
		missing: "no line of " + missing + " says why:\n  cmd/worker capability exec\n",
		stale:   "no binary reaches any more - remove them in this commit:\n  cmd/worker capability mount\n",
	} {
		var out, errs bytes.Buffer
		if code := run([]string{"-allow", list}, &out, &errs); code != 1 {
			t.Errorf("exit %d, want 1:\n%s%s", code, out.String(), errs.String())
		}
		if !strings.Contains(out.String(), want) {
			t.Errorf("the gate does not say %q:\n%s", want, out.String())
		}
	}
}

// No answer is exit 2, and says why: a list with a line that gives no reason, before anything is
// linked; a pattern with no binary; a binary that does not build.
func TestNoAnswerIsExit2(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go build")
	}
	noReason := allowWithout(t, "", "cmd/worker capability mount\n")
	broken := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":     "module example.com/broken\n\ngo 1.22\n",
		"cmd/x/x.go": "package main\n\nfunc main() { undefined() }\n",
		"lib/lib.go": "package lib\n",
	} {
		path := filepath.Join(broken, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(broken)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"-allow", noReason}, "has no reason after '#'"},
		{[]string{"-allow", filepath.Join(broken, "none.txt")}, "the allowlist is read on every run"},
		{[]string{"-roots", "./lib/..."}, "no main package under ./lib/..."},
		{[]string{"-skip", "./cmd/x"}, "no main package under ./..."},
		{[]string{}, "undefined: undefined"},
		{[]string{"-top"}, "flag needs an argument"},
	} {
		var out, errs bytes.Buffer
		if code := run(c.args, &out, &errs); code != 2 {
			t.Errorf("%v: exit %d, want 2:\n%s%s", c.args, code, out.String(), errs.String())
		}
		if !strings.Contains(errs.String(), c.want) {
			t.Errorf("%v does not say %q:\n%s", c.args, c.want, errs.String())
		}
	}
}

// A failed build's message is what go build said, without the graph the linker started printing.
func TestNotEdgesIsWhatTheBuildSaid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deps")
	if err := os.WriteFile(path, []byte("_ -> main.main\n# example.com/m\n\nmain.go:3: undefined: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := notEdges(path), "# example.com/m\nmain.go:3: undefined: x"; got != want {
		t.Errorf("notEdges = %q, want %q", got, want)
	}
	if got := notEdges(filepath.Join(t.TempDir(), "none")); !strings.Contains(got, "no such file") {
		t.Errorf("notEdges of no file = %q", got)
	}
}
