package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/sbom"
)

// Each kind writes a BOM of -name at -version from what it reads, that check passes; asked
// wrongly, it writes nothing and says 2.
func TestEachKindWritesACheckedBOM(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
		return p
	}
	self, err := os.Executable()
	require.NoError(t, err)
	lock := write("apko.lock.json", `{"contents":{"packages":[{"name":"busybox","version":"1.37.0-r50","architecture":"x86_64"}]}}`)
	pkgs := write("packages.txt", "bash 5.2 amd64\n")
	db := write("installed", "P:musl\nV:1.2.5-r10\nA:x86_64\n\nP:gcc\nV:15.1.0-r0\nA:x86_64\n")
	pnpm := write("pnpm-lock.yaml", "importers:\n  .:\n    dependencies:\n      react:\n        version: 19.2.0\npackages:\n  react@19.2.0:\n    resolution: {integrity: sha512-AAAA}\nsnapshots:\n  react@19.2.0: {}\n")
	pins := write("versions.yaml", "- name: snapshot\n  kind: date\n  version: 20261001T000000Z\n  track: today\n")
	blob := write("bios.bin", "seabios")
	for name, tc := range map[string]struct {
		args   []string
		status int
	}{
		"go":                  {args: []string{"go", self}},
		"apko":                {args: []string{"apko", lock}},
		"dpkg":                {args: []string{"dpkg", "-distro", "ubuntu", pkgs}},
		"apk":                 {args: []string{"apk", "-distro", "alpine", "-only", "musl", db}},
		"pnpm":                {args: []string{"pnpm", pnpm}},
		"pins":                {args: []string{"pins", "-versions", pins, "snapshot"}},
		"file":                {args: []string{"file", blob}},
		"dpkg with no distro": {args: []string{"dpkg", pkgs}, status: 2},
		"apk with no only":    {args: []string{"apk", "-distro", "alpine", db}, status: 2},
		"apk, one not there":  {args: []string{"apk", "-distro", "alpine", "-only", "zlib", db}, status: 2},
		"two locks":           {args: []string{"apko", lock, lock}, status: 2},
		"no lock":             {args: []string{"apko", filepath.Join(dir, "none")}, status: 2},
		"a pin not there":     {args: []string{"pins", "-versions", pins, "kernel"}, status: 2},
		"no versions.yaml":    {args: []string{"pins", "-versions", filepath.Join(dir, "none"), "x"}, status: 2},
		"a file not there":    {args: []string{"file", filepath.Join(dir, "none")}, status: 2},
		"two files":           {args: []string{"file", blob, blob}, status: 2},
		"a binary not there":  {args: []string{"go", filepath.Join(dir, "none")}, status: 2},
		"a broken lock":       {args: []string{"apko", write("broken.json", "{")}, status: 2},
		"a broken listing":    {args: []string{"dpkg", "-distro", "ubuntu", write("broken.txt", "bash\n")}, status: 2},
		"a broken pnpm lock":  {args: []string{"pnpm", write("broken.yaml", "{")}, status: 2},
		"no such kind":        {args: []string{"spdx", lock}, status: 2},
		"an unknown flag":     {args: []string{"apko", "-format", "xml", lock}, status: 2},
	} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "x.cdx.json")
			args := append([]string{tc.args[0], "-name", "spin", "-version", "v1", "-out", out}, tc.args[1:]...)
			var stdout, stderr bytes.Buffer
			require.Equal(t, tc.status, run(args, func(string) string { return "" }, &stdout, &stderr), stderr.String())
			_, err := os.Stat(out)
			if tc.status != 0 {
				assert.True(t, os.IsNotExist(err), "a BOM was written")
				return
			}
			b, err := sbom.Read(out)
			require.NoError(t, err)
			require.NoError(t, sbom.Check(b))
			assert.Equal(t, "spin", b.Metadata.Component.Name)
			assert.Equal(t, 0, run([]string{"check", out}, nil, &stdout, &stderr))
		})
	}
}

// -name, -version and -out are required, a SOURCE_DATE_EPOCH that is no number is refused, and
// with none of them a kind writes nothing.
func TestARunMissingWhatItNeedsWritesNothing(t *testing.T) {
	dir := t.TempDir()
	blob := filepath.Join(dir, "f")
	require.NoError(t, os.WriteFile(blob, []byte("x"), 0o600))
	out := filepath.Join(dir, "x.cdx.json")
	for name, tc := range map[string]struct {
		args  []string
		epoch string
	}{
		"nothing":          {},
		"no name":          {args: []string{"file", "-version", "v1", "-out", out, blob}},
		"no version":       {args: []string{"file", "-name", "f", "-out", out, blob}},
		"no out":           {args: []string{"file", "-name", "f", "-version", "v1", blob}},
		"a bad epoch":      {args: []string{"file", "-name", "f", "-version", "v1", "-out", out, blob}, epoch: "soon"},
		"an out not there": {args: []string{"file", "-name", "f", "-version", "v1", "-out", filepath.Join(dir, "no", "x.json"), blob}},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			assert.Equal(t, 2, run(tc.args, func(string) string { return tc.epoch }, &stdout, &stderr))
			_, err := os.Stat(out)
			assert.True(t, os.IsNotExist(err))
		})
	}
}

// A merge is the subject file by its hash, made of the BOMs it nests; without -subject, or with a
// BOM it cannot read, there is none.
func TestMergeNestsTheBOMsUnderTheFile(t *testing.T) {
	dir := t.TempDir()
	tarball := filepath.Join(dir, "f.tar.gz")
	require.NoError(t, os.WriteFile(tarball, []byte("tarball"), 0o600))
	part := filepath.Join(dir, "part.cdx.json")
	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, run([]string{"file", "-name", "bios.bin", "-version", "1", "-out", part, tarball}, func(string) string { return "" }, &stdout, &stderr))
	out := filepath.Join(dir, "f.cdx.json")
	require.Equal(t, 0, run([]string{"merge", "-name", "f.tar.gz", "-version", "v1", "-subject", tarball, "-out", out, part},
		func(string) string { return "1760000000" }, &stdout, &stderr), stderr.String())
	b, err := sbom.Read(out)
	require.NoError(t, err)
	assert.Equal(t, "db4b4d0d1cb480bf9aeea253771c00febe627f236765fa37d6a5614f079a3aa0", (*b.Metadata.Component.Hashes)[0].Value)
	assert.Equal(t, "2025-10-09T08:53:20Z", b.Metadata.Timestamp)
	assert.Equal(t, "bios.bin", (*b.Components)[0].Name)
	for name, args := range map[string][]string{
		"no subject":          {"merge", "-name", "f", "-version", "v1", "-out", out + "2", part},
		"a subject not there": {"merge", "-name", "f", "-version", "v1", "-subject", filepath.Join(dir, "none"), "-out", out + "2", part},
		"a BOM not there":     {"merge", "-name", "f", "-version", "v1", "-subject", tarball, "-out", out + "2", filepath.Join(dir, "none")},
	} {
		assert.Equal(t, 2, run(args, func(string) string { return "" }, &stdout, &stderr), name)
	}
}

// check is 0 on whole BOMs, 1 on one with a component no scanner could match, and 2 on what is
// not a BOM or on nothing to check.
func TestCheckSaysWhichBOMIsNotWhole(t *testing.T) {
	dir := t.TempDir()
	devel := filepath.Join(dir, "devel.cdx.json")
	require.NoError(t, os.WriteFile(devel, []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","version":1,
		"metadata":{"component":{"type":"application","name":"grafana","version":"(devel)","purl":"pkg:golang/grafana"}}}`), 0o600))
	var stdout, stderr bytes.Buffer
	assert.Equal(t, 1, run([]string{"check", devel}, nil, &stdout, &stderr))
	assert.Contains(t, stdout.String(), "grafana")
	assert.Equal(t, 2, run([]string{"check"}, nil, &stdout, &stderr))
	assert.Equal(t, 2, run([]string{"check", filepath.Join(dir, "none")}, nil, &stdout, &stderr))
}
