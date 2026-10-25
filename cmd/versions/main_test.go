package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/versions"
)

// stdout is what f printed to os.Stdout.
func stdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	was := os.Stdout
	os.Stdout = w
	f()
	os.Stdout = was
	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

// Each command answers from the file it is given, and refuses what it is not asked as it reads:
// no command, an unknown one, a flag it has not, a file not there, a name it needs and lacks.
// check and bump reach upstreams, so only what they refuse before that is asked here.
func TestTheCommandsAnswerFromTheFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "versions.yaml")
	require.NoError(t, os.WriteFile(file, []byte(`
- name: kernel
  kind: download
  source: https://cdn.kernel.org/pub/linux/kernel/v{major}.x/linux-{version}.tar.xz
  version: 7.2.8
  pin: 1111111111111111111111111111111111111111111111111111111111111111
  track: kernel-stable
`), 0o600))
	for name, tc := range map[string]struct {
		argv  []string
		out   string
		fails bool
	}{
		"args":            {argv: []string{"args", "-file", file, "kernel"}, out: "--build-arg KERNEL_SHA256=1111111111111111111111111111111111111111111111111111111111111111 --build-arg KERNEL_VERSION=7.2.8\n"},
		"env":             {argv: []string{"env", "-file", file, "kernel"}, out: "KERNEL_SHA256=1111111111111111111111111111111111111111111111111111111111111111\nKERNEL_VERSION=7.2.8\n"},
		"version":         {argv: []string{"version", "-file", file, "kernel"}, out: "7.2.8\n"},
		"newer":           {argv: []string{"newer", "-file", file, "kernel", "7.2.9"}, out: "7.2.9\n"},
		"not newer":       {argv: []string{"newer", "-file", file, "kernel", "7.2.8"}},
		"newer, no form":  {argv: []string{"newer", "-file", file, "kernel", "v7.2.9"}, fails: true},
		"newer, no name":  {argv: []string{"newer", "-file", file, "kernel"}, fails: true},
		"newer, no entry": {argv: []string{"newer", "-file", file, "nope", "1"}, fails: true},
		"url":             {argv: []string{"url", "-file", file, "kernel"}, out: "https://cdn.kernel.org/pub/linux/kernel/v7.x/linux-7.2.8.tar.xz\n"},
		"help":            {argv: []string{"help"}, out: usage},
		"-h":              {argv: []string{"-h"}, out: usage},
		"--help":          {argv: []string{"--help"}, out: usage},
		"nothing":         {fails: true},
		"an unknown one":  {argv: []string{"frob", "-file", file}, fails: true},
		"an unknown flag": {argv: []string{"version", "-nope"}, fails: true},
		"help as a flag":  {argv: []string{"version", "-h"}, out: usage, fails: true},
		"no file":         {argv: []string{"version", "-file", filepath.Join(t.TempDir(), "none"), "kernel"}, fails: true},
		"args of nothing": {argv: []string{"args", "-file", file}, fails: true},
		"args of none":    {argv: []string{"args", "-file", file, "qemu"}, fails: true},
		"check of names":  {argv: []string{"check", "-file", file, "kernel"}, fails: true},
		"bump of nothing": {argv: []string{"bump", "-file", file}, fails: true},
		"bump of three":   {argv: []string{"bump", "-file", file, "a", "b", "c"}, fails: true},
		"bump of none":    {argv: []string{"bump", "-file", file, "qemu"}, fails: true},
	} {
		t.Run(name, func(t *testing.T) {
			var err error
			out := stdout(t, func() { err = run(t.Context(), tc.argv) })
			assert.Equal(t, tc.fails, err != nil, "err = %v", err)
			assert.Equal(t, tc.out, out)
		})
	}
}

// One entry says its version, an image its reference and a download its URL; asked what its kind
// has not, or of no entry or two, it says nothing.
func TestOneEntrySaysWhatItsKindHas(t *testing.T) {
	v, err := versions.Parse([]byte(`
- name: kernel
  kind: download
  source: https://git.kernel.org/torvalds/t/linux-{version}.tar.gz
  version: 7.3-rc5
  pin: 1111111111111111111111111111111111111111111111111111111111111111
  track: kernel-stable
- name: alpine
  kind: image
  source: alpine
  version: "3.24"
  pin: sha256:2222222222222222222222222222222222222222222222222222222222222222
  track: digest
`))
	require.NoError(t, err)
	for name, tc := range map[string]struct {
		cmd, entry, out string
	}{
		"a version":        {cmd: "version", entry: "kernel", out: "7.3-rc5\n"},
		"a download's URL": {cmd: "url", entry: "kernel", out: "https://git.kernel.org/torvalds/t/linux-7.3-rc5.tar.gz\n"},
		"an image's ref":   {cmd: "ref", entry: "alpine", out: "alpine:3.24@sha256:2222222222222222222222222222222222222222222222222222222222222222\n"},
		"an image's URL":   {cmd: "url", entry: "alpine"},
		"a download's ref": {cmd: "ref", entry: "kernel"},
		"no such entry":    {cmd: "version", entry: "qemu"},
	} {
		t.Run(name, func(t *testing.T) {
			var w bytes.Buffer
			err := one(&w, v, tc.cmd, []string{tc.entry})
			if tc.out == "" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.out, w.String())
		})
	}
	require.Error(t, one(&bytes.Buffer{}, v, "version", nil))
	require.Error(t, one(&bytes.Buffer{}, v, "version", []string{"kernel", "alpine"}))
}
