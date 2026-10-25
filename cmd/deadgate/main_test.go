package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// module is a repository of two binaries and three packages: cmd/a reaches lib.Used and not
// lib.Unused, nothing imports orphan, and cmd/skipped is the binary that is no root.
func module(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":                 "module example.com/m\n\ngo 1.22\n",
		"cmd/a/main.go":          "package main\n\nimport \"example.com/m/lib\"\n\nfunc main() { lib.Used() }\n",
		"cmd/skipped/main.go":    "package main\n\nfunc main() {}\n",
		"lib/lib.go":             "package lib\n\nfunc Used()   {}\nfunc Unused() {}\n",
		"orphan/orphan.go":       "package orphan\n",
		"cmd/skipped/own/own.go": "package own\n\nfunc Own() {}\n",
	}
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// fakeDeadcode is a deadcode that reports what it is told to, and records how it was called.
func fakeDeadcode(t *testing.T, dir, report string) string {
	t.Helper()
	path := filepath.Join(dir, "deadcode")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > " + filepath.Join(dir, "deadcode.args") + "\nprintf '" + report + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { //nolint:gosec // the fake is run
		t.Fatal(err)
	}
	return path
}

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The command's answer is the report over what deadcode said of the roots - the binaries but the
// skipped one, built first - and the packages no root imports, held to the two lists; deadcode is
// run with the module anchored and the roots as go names them.
func TestTheGateReadsTheRootsAndTheTwoLists(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go on PATH")
	}
	dir := module(t)
	deadcode := fakeDeadcode(t, dir, `example.com/m/lib.Unused\nexample.com/m/cmd/skipped/own.Own\n`)
	allow := write(t, dir, "allow.txt", "lib.Unused  # the test's own\n")
	pending := write(t, dir, "pending.txt", "package orphan  # being removed\n")
	stale := write(t, dir, "stale.txt", "lib.Unused  # the test's own\nlib.Gone  # was\n")
	empty := write(t, dir, "empty.txt", "# nothing\n")
	noReason := write(t, dir, "noreason.txt", "lib.Unused\n")
	t.Chdir(dir)
	for name, tc := range map[string]struct {
		args   []string
		status int
		out    string
		err    string
	}{
		"green":       {args: []string{"-deadcode", deadcode, "-skip", "./cmd/skipped", "-allow", allow, "-pending", pending}, out: "reported: 2    explained by " + allow + ": 1    owed a deletion in " + pending + ": 1    unexplained: 0\nOK: every unreachable symbol has a recorded reason, and every reason still applies.\n\nstill owed a deletion (" + pending + ") - the ratchet is not at zero:\n  package orphan\n"},
		"unexplained": {args: []string{"-deadcode", deadcode, "-skip", "./cmd/skipped", "-allow", empty, "-pending", empty}, status: 1, out: "no binary reaches these, and nothing says why that is acceptable:\n  lib.Unused\n  package orphan\n"},
		// cmd/skipped is a root here, and its package is reached by nothing: a finding, as deadcode said.
		"nothing skipped":   {args: []string{"-deadcode", deadcode, "-allow", allow, "-pending", pending}, status: 1, out: "  cmd/skipped/own.Own\n"},
		"stale":             {args: []string{"-deadcode", deadcode, "-skip", "./cmd/skipped", "-allow", stale, "-pending", pending}, status: 1, out: "stale entries in " + stale + " - deadcode no longer reports these"},
		"no reason":         {args: []string{"-deadcode", deadcode, "-allow", noReason, "-pending", pending}, status: 2, err: "has no reason"},
		"no lists":          {args: []string{"-deadcode", deadcode}, status: 2, err: "-allow and -pending"},
		"no pending":        {args: []string{"-deadcode", deadcode, "-allow", allow}, status: 2, err: "-allow and -pending"},
		"no allow":          {args: []string{"-deadcode", deadcode, "-pending", pending}, status: 2, err: "-allow and -pending"},
		"a flag it has not": {args: []string{"-nope"}, status: 2},
		"no deadcode":       {args: []string{"-deadcode", filepath.Join(dir, "none"), "-allow", allow, "-pending", pending}, status: 2, err: "none"},
		"no root":           {args: []string{"-deadcode", deadcode, "-roots", "./lib/...", "-allow", allow, "-pending", pending}, status: 2, err: "no main package under ./lib/... is a root"},
		"no such packages":  {args: []string{"-deadcode", deadcode, "-packages", "./nowhere/...", "-allow", allow, "-pending", pending}, status: 2, err: "go list"},
	} {
		t.Run(name, func(t *testing.T) {
			stdout, stderr := capture(t)
			status := run(tc.args, stdout.w, stderr.w)
			out, errText := stdout.read(t), stderr.read(t)
			if status != tc.status {
				t.Errorf("status %d, want %d; stderr:\n%s", status, tc.status, errText)
			}
			if !strings.Contains(out, tc.out) {
				t.Errorf("stdout lacks %q:\n%s", tc.out, out)
			}
			if !strings.Contains(errText, tc.err) {
				t.Errorf("stderr lacks %q:\n%s", tc.err, errText)
			}
		})
	}
	args, err := os.ReadFile(filepath.Join(dir, "deadcode.args"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `-filter ^example.com/m/ -f={{range .Funcs}}{{$.Path}}.{{.Name}}{{"\n"}}{{end}} ./cmd/a`; !strings.HasPrefix(string(args), want) {
		t.Errorf("deadcode was run with %q, want it to begin %q", args, want)
	}
	// The roots did not build: the report would be of a program missing a package.
	write(t, dir, "lib/broken.go", "package lib\n\nfunc Broken() { undefined() }\n")
	stdout, stderr := capture(t)
	if status := run([]string{"-deadcode", deadcode, "-allow", allow, "-pending", pending}, stdout.w, stderr.w); status != 2 {
		t.Errorf("status %d with roots that do not build", status)
	}
	if text := stderr.read(t); !strings.Contains(text, "the roots do not build") || !strings.Contains(text, "undefined") {
		t.Errorf("stderr: %s", text)
	}
	_ = stdout.read(t)
}

// pipe is one end of what run writes.
type pipe struct{ r, w *os.File }

func capture(t *testing.T) (stdout, stderr pipe) {
	t.Helper()
	for _, p := range []*pipe{&stdout, &stderr} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		*p = pipe{r, w}
	}
	return stdout, stderr
}

// read is what was written, once the writer is closed; a report fits a pipe's buffer.
func (p pipe) read(t *testing.T) string {
	t.Helper()
	if err := p.w.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(p.r)
	if err != nil {
		t.Fatal(err)
	}
	_ = p.r.Close()
	return string(b)
}
