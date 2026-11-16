package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, text string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func goroot(t *testing.T) string {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "LICENSE"), "Go's BSD license\n")
	write(t, filepath.Join(dir, "PATENTS"), "Go's patent grant\n")
	return dir
}

// Each module's license is carried once, under every license its file names, with the NOTICE
// beside it; Go's own comes first, and the modules follow sorted, whatever order they came in.
func TestEachModuleCarriesItsLicenseAndNotice(t *testing.T) {
	mods := t.TempDir()
	zlic := write(t, filepath.Join(mods, "z", "LICENSE"), "z's MIT license\n")
	alic := write(t, filepath.Join(mods, "a", "LICENSE.txt"), "a's Apache license\n")
	write(t, filepath.Join(mods, "a", "NOTICE"), "a's notice\n")
	in := "example.com/z\tv1.0.0\tMIT\t" + zlic + "\n" +
		"example.com/a\tv2.0.0\tApache-2.0\t" + alic + "\n" +
		"example.com/z\tv1.0.0\tBSD-3-Clause\t" + zlic + "\n"
	var out bytes.Buffer
	if err := run([]string{"-go", "go1.27.4", "-goroot", goroot(t)}, strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	rule := strings.Repeat("=", 80)
	for _, want := range []string{
		rule + "\nGo standard library and runtime go1.27.4 (BSD-3-Clause)\n" + rule + "\n\nGo's BSD license\n\nGo's patent grant\n",
		rule + "\nexample.com/a v2.0.0 (Apache-2.0)\n" + rule + "\n\na's Apache license\n\na's notice\n",
		rule + "\nexample.com/z v1.0.0 (MIT, BSD-3-Clause)\n" + rule + "\n\nz's MIT license\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing\n%s\nin\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, header) {
		t.Errorf("does not open with the header:\n%s", got)
	}
	if strings.Count(got, "z's MIT license") != 1 {
		t.Errorf("z's license is not there once:\n%s", got)
	}
	if goAt, a, z := strings.Index(got, "Go standard"), strings.Index(got, "example.com/a"), strings.Index(got, "example.com/z"); goAt >= a || a >= z {
		t.Errorf("not Go, a, z in that order:\n%s", got)
	}
}

// A module go-licenses could not name a license for, or find a file of, stops the archive: its
// notice is one it could not carry.
func TestAModuleWithoutANamedLicenseIsAnError(t *testing.T) {
	lic := write(t, filepath.Join(t.TempDir(), "LICENSE"), "text\n")
	for name, in := range map[string]string{
		"unknown license": "example.com/m\tv1\tUnknown\t" + lic + "\n",
		"unknown file":    "example.com/m\tv1\tMIT\tUnknown\n",
		"no file":         "example.com/m\tv1\tMIT\t\n",
		"not four fields": "example.com/m\tv1\tMIT\n",
		"a missing file":  "example.com/m\tv1\tMIT\t/nonexistent/LICENSE\n",
	} {
		t.Run(name, func(t *testing.T) {
			err := run([]string{"-go", "go1", "-goroot", goroot(t)}, strings.NewReader(in), &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "example.com/m") {
				t.Errorf("err = %v, want one naming example.com/m", err)
			}
		})
	}
}

// Without Go's version and GOROOT the notices would leave out what every binary carries; an
// argument besides them is a mistake, not a module.
func TestGoIsRequired(t *testing.T) {
	root := goroot(t)
	for _, args := range [][]string{nil, {"-go", "go1"}, {"-goroot", root}, {"-go", "go1", "-goroot", root, "extra"}} {
		if err := run(args, strings.NewReader(""), &bytes.Buffer{}); err == nil {
			t.Errorf("run(%q) = nil, want an error", args)
		}
	}
	if err := run([]string{"-go", "go1", "-goroot", t.TempDir()}, strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Error("a GOROOT without LICENSE gave no error")
	}
}
