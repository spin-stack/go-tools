package deadgate_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spin-stack/go-tools/internal/deadgate"
)

func set(keys ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// A finding is explained by either list, an entry has to match a finding, and nothing may be in
// both: each failure in its own place, sorted, and the pending list whole for the green run.
func TestTheRatchetTurnsOneWay(t *testing.T) {
	found := []string{"pkg/b.Gone", "pkg/a.Unused", "package pkg/c", "pkg/d.Twice"}
	r := deadgate.Judge(found, set("pkg/a.Unused", "pkg/d.Twice", "pkg/x.Deleted"), set("package pkg/c", "pkg/d.Twice", "pkg/y.Done"))
	if r.Found != 4 || r.Allowed != 3 || r.Pending != 3 {
		t.Errorf("counts %d %d %d", r.Found, r.Allowed, r.Pending)
	}
	for name, got := range map[string][]string{
		"unexplained":   r.Unexplained,
		"stale allowed": r.StaleAllowed,
		"stale pending": r.StalePending,
		"both":          r.Both,
		"owed":          r.Owed,
	} {
		want := map[string]string{
			"unexplained":   "pkg/b.Gone",
			"stale allowed": "pkg/x.Deleted",
			"stale pending": "pkg/y.Done",
			"both":          "pkg/d.Twice",
			"owed":          "package pkg/c pkg/d.Twice pkg/y.Done",
		}[name]
		if strings.Join(got, " ") != want {
			t.Errorf("%s: %v, want %s", name, got, want)
		}
	}
	if r.OK() {
		t.Error("a report with failures is OK")
	}
	var out bytes.Buffer
	r.Write(&out, "allow.txt", "pending.txt")
	text := out.String()
	for _, want := range []string{
		"reported: 4    explained by allow.txt: 3    owed a deletion in pending.txt: 3    unexplained: 1\n",
		"in BOTH lists", "\n  pkg/d.Twice\n",
		"stale entries in allow.txt", "\n  pkg/x.Deleted\n",
		"in pending.txt and are no longer reported", "\n  pkg/y.Done\n",
		"nothing says why that is acceptable:\n  pkg/b.Gone\n",
		"or add it to allow.txt if", "or add it to pending.txt if",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the report lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "OK:") || strings.Contains(text, "still owed") {
		t.Errorf("a failing report says OK:\n%s", text)
	}
}

// Any one failure alone is a failure.
func TestOneFailureAloneFails(t *testing.T) {
	for name, r := range map[string]deadgate.Report{
		"unexplained":   deadgate.Judge([]string{"a.X"}, nil, nil),
		"stale allowed": deadgate.Judge(nil, set("a.X"), nil),
		"stale pending": deadgate.Judge(nil, nil, set("a.X")),
		"both":          deadgate.Judge([]string{"a.X"}, set("a.X"), set("a.X")),
	} {
		if r.OK() {
			t.Errorf("%s alone is OK", name)
		}
	}
}

// A green run says so, and prints what is still owed - the only kind of run anybody has once
// this is a gate, so the debt is shown where it is read.
func TestAGreenRunPrintsWhatItStillOwes(t *testing.T) {
	r := deadgate.Judge([]string{"pkg/a.Fine", "pkg/b.Owed"}, set("pkg/a.Fine"), set("pkg/b.Owed"))
	if !r.OK() {
		t.Fatalf("not OK: %+v", r)
	}
	var out bytes.Buffer
	r.Write(&out, "allow.txt", "pending.txt")
	if text := out.String(); !strings.HasSuffix(text, "OK: every unreachable symbol has a recorded reason, and every reason still applies.\n\nstill owed a deletion (pending.txt) - the ratchet is not at zero:\n  pkg/b.Owed\n") {
		t.Errorf("unexpected report:\n%s", text)
	}
	r = deadgate.Judge([]string{"pkg/a.Fine"}, set("pkg/a.Fine"), nil)
	out.Reset()
	r.Write(&out, "allow.txt", "pending.txt")
	if text := out.String(); !strings.HasSuffix(text, "every reason still applies.\npending.txt is empty: nothing unreachable is waiting on a decision.\n") {
		t.Errorf("unexpected report:\n%s", text)
	}
	if r := deadgate.Judge(nil, nil, nil); !r.OK() || r.Found != 0 {
		t.Errorf("nothing found is not OK: %+v", r)
	}
}

// deadcode's lines become the module's names: the module path off, a skipped binary's own
// packages out, sorted and once each.
func TestSymbolsAreTheModulesNames(t *testing.T) {
	out := "m/internal/b.Two\nm/cmd/proxy/modules.Own\n\nm/internal/a.One\nm/internal/b.Two\n  m/cmd/proxyish.Kept  \n"
	got := deadgate.Symbols(out, "m", []string{"cmd/proxy"})
	if strings.Join(got, " ") != "cmd/proxyish.Kept internal/a.One internal/b.Two" {
		t.Errorf("got %v", got)
	}
	if got := deadgate.Symbols("", "m", nil); len(got) != 0 {
		t.Errorf("nothing reported is %v", got)
	}
}

// A package no root depends on is reported as a package, the module's own path as "."; a skipped
// binary's own packages are not.
func TestAPackageNoRootImportsIsAFinding(t *testing.T) {
	all := []string{"m/internal/a", "m/internal/b", "m", "m/internal/b", "m/cmd/proxy", "m/cmd/proxy/modules", "m/cmd/proxyish"}
	deps := []string{"m/internal/a", "m/cmd/x", "other/dep"}
	got := deadgate.Unimported(all, deps, "m", []string{"cmd/proxy"})
	if strings.Join(got, " ") != "package . package cmd/proxyish package internal/b" {
		t.Errorf("got %v", got)
	}
}

// The roots are every main package but the skipped, as go is given them.
func TestTheRootsAreEveryMainButTheSkipped(t *testing.T) {
	got := deadgate.Roots([]string{"m/cmd/b", "m/cmd/proxy", "m/cmd/a", "m/cmd/a"}, "m", []string{"./cmd/proxy"})
	if strings.Join(got, " ") != "./cmd/a ./cmd/b" {
		t.Errorf("got %v", got)
	}
}
