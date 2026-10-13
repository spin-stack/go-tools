package sbom_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	cdx "github.com/CycloneDX/cyclonedx-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/sbom"
	"github.com/spin-stack/go-tools/versions"
)

func component(name, version, purl string) cdx.Component {
	return cdx.Component{Type: cdx.ComponentTypeLibrary, Name: name, Version: version, PackageURL: purl}
}

// The same inputs are the same bytes, whatever order the components came in; the serial is the
// subject's, and the one time in it is the epoch's.
func TestABOMIsAFunctionOfItsInputs(t *testing.T) {
	subject := cdx.Component{Type: cdx.ComponentTypeApplication, Name: "spin-controlplane", Version: "v20261013.1",
		Hashes: &[]cdx.Hash{{Algorithm: cdx.HashAlgoSHA256, Value: strings.Repeat("a", 64)}}}
	a, b := component("a", "1", "pkg:golang/a@1"), component("b", "2", "pkg:golang/b@2")
	epoch := time.Unix(1760000000, 0)
	encode := func(cs ...cdx.Component) string {
		var w bytes.Buffer
		require.NoError(t, sbom.Encode(&w, sbom.New(subject, cs, nil, epoch)))
		return w.String()
	}
	first, second := encode(a, b), encode(b, a)
	assert.Equal(t, first, second)
	assert.Contains(t, first, `"specVersion": "1.6"`)
	assert.Contains(t, first, `"timestamp": "2025-10-09T08:53:20Z"`)

	other := subject
	other.Version = "v20261013.2"
	assert.NotEqual(t, sbom.New(subject, nil, nil, epoch).SerialNumber, sbom.New(other, nil, nil, epoch).SerialNumber)
	assert.Regexp(t, `^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-8[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, sbom.New(subject, nil, nil, epoch).SerialNumber)
	assert.Empty(t, sbom.New(subject, nil, nil, time.Time{}).Metadata.Timestamp)
}

// SOURCE_DATE_EPOCH is the build's time; unset, none; anything but seconds, an error.
func TestTheEpochIsSourceDateEpochs(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want time.Time
		bad  bool
	}{
		{env: ""}, {env: "1760000000", want: time.Unix(1760000000, 0)},
		{env: "17e8", bad: true}, {env: "soon", bad: true}, {env: "+1", bad: true},
	} {
		got, err := sbom.Epoch(func(string) string { return tc.env })
		if tc.bad {
			assert.Error(t, err, tc.env)
			continue
		}
		require.NoError(t, err, tc.env)
		assert.True(t, got.Equal(tc.want), tc.env)
	}
}

// Components that share a package URL are ordered by name, then version, nested ones too.
func TestComponentsAreOrderedByPURLNameAndVersion(t *testing.T) {
	cs := []cdx.Component{
		component("b", "1", "pkg:x/p"), component("a", "2", "pkg:x/p"), component("a", "1", "pkg:x/p"), component("z", "1", "pkg:a/p"),
	}
	parent := component("parent", "1", "pkg:x/parent")
	nested := []cdx.Component{component("n", "2", "pkg:x/n"), component("n", "1", "pkg:x/n")}
	parent.Components = &nested
	b := sbom.New(component("s", "1", "pkg:s"), append(cs, parent), nil, time.Time{})
	var order []string
	for _, c := range *b.Components {
		order = append(order, c.Name+"@"+c.Version)
	}
	assert.Equal(t, []string{"z@1", "a@1", "a@2", "b@1", "parent@1"}, order)
	got := (*b.Components)[4]
	assert.Equal(t, "1", (*got.Components)[0].Version)
}

// Every serial is an RFC 9562 UUID of version 8, whatever the subject, and one subject's is the
// same serial on every build.
func TestEverySerialIsAVersion8UUID(t *testing.T) {
	// sha256("spin\nv20261013.1\npkg:generic/spin@v20261013.1\n")[:16], 9f66d0181329337dd4cf31fd9f9b2f9d,
	// with byte 6's high nibble 8 and byte 8's two high bits 10.
	assert.Equal(t, "urn:uuid:9f66d018-1329-837d-94cf-31fd9f9b2f9d", sbom.New(component("spin", "v20261013.1", "pkg:generic/spin@v20261013.1"), nil, nil, time.Time{}).SerialNumber)
	for i := range 64 {
		s := sbom.New(component(fmt.Sprint("subject", i), "1", "pkg:s"), nil, nil, time.Time{}).SerialNumber
		require.Regexp(t, `^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-8[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, s)
	}
}

// What a scanner cannot match - no version, Go's (devel), nothing to know the component by - fails
// the check, nested or in the formulation; a whole BOM passes, and one read back is the same.
func TestCheckRefusesWhatNoScannerCouldMatch(t *testing.T) {
	subject := component("spin", "v1", "pkg:golang/spin@v1")
	nested := component("app", "v1", "pkg:golang/app@v1")
	for name, tc := range map[string]struct {
		components, formulation []cdx.Component
		subject                 cdx.Component
		fails                   string
	}{
		"whole":                 {components: []cdx.Component{component("a", "1", "pkg:apk/wolfi/a@1")}, subject: subject},
		"no version":            {components: []cdx.Component{component("a", "", "pkg:apk/wolfi/a")}, subject: subject, fails: "a: version"},
		"(devel)":               {components: []cdx.Component{component("grafana", "(devel)", "pkg:golang/grafana")}, subject: subject, fails: "grafana: version"},
		"neither purl nor hash": {components: []cdx.Component{component("blob", "1", "")}, subject: subject, fails: "no package URL and no hash"},
		"a hash is enough": {subject: subject, components: []cdx.Component{{Type: cdx.ComponentTypeFile, Name: "bios.bin", Version: "11.1.2",
			Hashes: &[]cdx.Hash{{Algorithm: cdx.HashAlgoSHA256, Value: strings.Repeat("b", 64)}}}}},
		"nested": {subject: subject, components: []cdx.Component{func() cdx.Component {
			c := nested
			c.Components = &[]cdx.Component{component("dep", "", "pkg:golang/dep")}
			return c
		}()}, fails: "app/dep: version"},
		"in the formulation": {subject: subject, formulation: []cdx.Component{component("gcc", "", "pkg:apk/alpine/gcc")}, fails: "formulation/gcc"},
		"the subject's own":  {subject: component("spin", "(devel)", "pkg:golang/spin"), fails: "spin: version"},
		"a nameless one":     {subject: subject, components: []cdx.Component{component("", "1", "pkg:x/y@1")}, fails: "no name"},
	} {
		t.Run(name, func(t *testing.T) {
			b := sbom.New(tc.subject, tc.components, tc.formulation, time.Time{})
			path := filepath.Join(t.TempDir(), "x.cdx.json")
			require.NoError(t, sbom.Write(path, b))
			back, err := sbom.Read(path)
			require.NoError(t, err)
			err = sbom.Check(back)
			if tc.fails == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.fails)
		})
	}
	require.Error(t, sbom.Check(&cdx.BOM{}), "a BOM with no subject")
}

// A file that is not a BOM, or not there, is no BOM.
func TestReadRefusesWhatIsNotABOM(t *testing.T) {
	dir := t.TempDir()
	notBOM, bad := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	require.NoError(t, os.WriteFile(notBOM, []byte(`{"specVersion":"1.6"}`), 0o600))
	require.NoError(t, os.WriteFile(bad, []byte(`{`), 0o600))
	for _, p := range []string{notBOM, bad, filepath.Join(dir, "none.json")} {
		_, err := sbom.Read(p)
		require.Error(t, err, p)
	}
	require.Error(t, sbom.Write(filepath.Join(dir, "no", "x.json"), sbom.New(component("a", "1", "p"), nil, nil, time.Time{})))
}

// A merge is the subjects of the BOMs it is given, each with its components under it, and their
// formulations together; a BOM with no subject cannot be merged.
func TestMergeNestsEachBOMUnderItsSubject(t *testing.T) {
	cp := sbom.New(component("controlplane", "v1", "pkg:golang/cp@v1"), []cdx.Component{component("x", "1", "pkg:golang/x@1")},
		[]cdx.Component{component("go", "1.27.2", "pkg:golang/stdlib@1.27.2")}, time.Time{})
	machine := sbom.New(component("machine", "v2", "pkg:generic/machine@v2"), []cdx.Component{component("qemu", "11.1.2", "pkg:generic/qemu@11.1.2")}, nil, time.Time{})
	m, err := sbom.Merge(component("spin-controlplane-linux-amd64.tar.gz", "v1", "pkg:generic/spin-controlplane@v1"), time.Time{}, machine, cp)
	require.NoError(t, err)
	require.NoError(t, sbom.Check(m))
	require.Len(t, *m.Components, 2)
	nested := map[string]string{}
	for _, c := range *m.Components {
		nested[c.Name] = (*c.Components)[0].Name
	}
	assert.Equal(t, map[string]string{"controlplane": "x", "machine": "qemu"}, nested)
	assert.Equal(t, "go", (*(*m.Formulation)[0].Components)[0].Name)
	_, err = sbom.Merge(component("t", "1", "p"), time.Time{}, &cdx.BOM{})
	require.Error(t, err)
}

// A Go binary is its main module at the release's version, by its hash, made of the modules its
// build info lists and the standard library it was built with.
func TestAGoBinaryIsItsBuildInfo(t *testing.T) {
	self, err := os.Executable()
	require.NoError(t, err)
	c, err := sbom.Go("v20261013.1", self)
	require.NoError(t, err)
	assert.Equal(t, "v20261013.1", c.Version)
	assert.Equal(t, filepath.Base(self), c.Name)
	assert.Regexp(t, `^pkg:golang/.+@v20261013\.1$`, c.PackageURL)
	assert.Len(t, (*c.Hashes)[0].Value, 64)
	var names []string
	for _, d := range *c.Components {
		names = append(names, d.Name)
	}
	assert.Contains(t, names, "stdlib")
	assert.Contains(t, names, "github.com/stretchr/testify")
	_, err = sbom.Go("v1", filepath.Join(t.TempDir(), "none"))
	require.Error(t, err)
	notGo := filepath.Join(t.TempDir(), "sh")
	require.NoError(t, os.WriteFile(notGo, []byte("#!/bin/sh\n"), 0o600))
	_, err = sbom.Go("v1", notGo)
	require.Error(t, err)
}

// A file is a component by its SHA-256.
func TestAFileIsItsHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bios.bin")
	require.NoError(t, os.WriteFile(path, []byte("seabios"), 0o600))
	c, err := sbom.File("bios.bin", "11.1.2", path)
	require.NoError(t, err)
	assert.Equal(t, []cdx.Hash{{Algorithm: cdx.HashAlgoSHA256, Value: "142fb0a991a46a2d4283ab19aa220cdcadb629a5e17a0778db8affe32c6a1488"}}, *c.Hashes)
	assert.Equal(t, "11.1.2", c.Version)
	_, err = sbom.File("x", "1", filepath.Join(t.TempDir(), "none"))
	require.Error(t, err)
}

// An apko lock's packages are Wolfi's, each by its data section's SHA-256.
func TestApkoLockPackagesAreWolfis(t *testing.T) {
	lock := `{"contents":{"packages":[
		{"name":"busybox","version":"1.37.0-r50","architecture":"x86_64","data":{"checksum":"sha256-GUiGMWhlBBd1oq+UTUb+B5DlF9Qi2CshfZZfAIFZq+Y="}},
		{"name":"glibc","version":"2.42-r3","architecture":"x86_64","data":{"checksum":""}}]}}`
	cs, err := sbom.Apko([]byte(lock))
	require.NoError(t, err)
	require.Len(t, cs, 2)
	assert.Equal(t, "pkg:apk/wolfi/busybox@1.37.0-r50?arch=x86_64", cs[0].PackageURL)
	assert.Equal(t, "194886316865041775a2af944d46fe0790e517d422d82b217d965f008159abe6", (*cs[0].Hashes)[0].Value)
	assert.Nil(t, cs[1].Hashes)
	for _, bad := range []string{`{`, `{"contents":{"packages":[{"name":"x","data":{"checksum":"sha256-%%%"}}]}}`} {
		_, err := sbom.Apko([]byte(bad))
		require.Error(t, err, bad)
	}
}

// A dpkg listing is the distribution's packages; a line that is not one is an error.
func TestDpkgListingIsTheDistributions(t *testing.T) {
	cs, err := sbom.Dpkg([]byte("bash 5.2.21-2ubuntu4 amd64\n\nlibc6 2.39-0ubuntu8.6 amd64\n"), "ubuntu")
	require.NoError(t, err)
	require.Len(t, cs, 2)
	assert.Equal(t, "pkg:deb/ubuntu/bash@5.2.21-2ubuntu4?arch=amd64", cs[0].PackageURL)
	assert.Equal(t, [2]string{"libc6", "2.39-0ubuntu8.6"}, [2]string{cs[1].Name, cs[1].Version})
	_, err = sbom.Dpkg([]byte("bash 5.2 amd64\nbash 5.2\n"), "ubuntu")
	require.ErrorContains(t, err, "line 2")
}

// A module replaced is the replacement, which is what was compiled, with the go.sum hash it was
// checked against; the standard library is the toolchain's.
func TestModulesAreWhatWasCompiled(t *testing.T) {
	info := &debug.BuildInfo{GoVersion: "go1.27.2", Deps: []*debug.Module{
		{Path: "golang.org/x/net", Version: "v0.60.0", Sum: "h1:abc="},
		{Path: "github.com/a/b", Version: "v1.0.0", Replace: &debug.Module{Path: "github.com/fork/b", Version: "v1.0.1", Sum: "h1:def="}},
	}}
	got := map[string]string{}
	for _, c := range sbom.Modules(info) {
		sum := ""
		if c.Properties != nil {
			sum = (*c.Properties)[0].Value
		}
		got[c.PackageURL] = sum
	}
	assert.Equal(t, map[string]string{
		"pkg:golang/stdlib@1.27.2": "", "pkg:golang/golang.org/x/net@v0.60.0": "h1:abc=", "pkg:golang/github.com/fork/b@v1.0.1": "h1:def=",
	}, got)
}

// Of an apk database, the packages named are what is shipped and the rest what built it; a name
// the database lacks is an error.
func TestApkDatabaseSplitsShippedFromBuilt(t *testing.T) {
	db := "C:Q1abc=\nP:glib-static\nV:2.84.4-r0\nA:x86_64\n\nP:python3\nV:3.12.11-r0\nA:x86_64\n\nP:musl\nV:1.2.5-r10\nA:x86_64\n"
	shipped, built, err := sbom.Apk([]byte(db), "alpine", []string{"glib-static", "musl"})
	require.NoError(t, err)
	require.Len(t, shipped, 2)
	require.Len(t, built, 1)
	assert.Equal(t, "pkg:apk/alpine/glib-static@2.84.4-r0?arch=x86_64", shipped[0].PackageURL)
	assert.Equal(t, "python3", built[0].Name)
	_, _, err = sbom.Apk([]byte(db), "alpine", []string{"pixman-static"})
	require.Error(t, err)
}

// A pnpm lock's root dependencies and theirs are what runs - its devDependencies are not - each
// by its integrity's SHA-512, a package resolved twice against different peers once.
func TestPnpmLockIsWhatTheProjectRunsWith(t *testing.T) {
	lock := `
lockfileVersion: '9.0'
importers:
  .:
    dependencies:
      '@connectrpc/connect':
        specifier: ^2.2.0
        version: 2.2.0(@bufbuild/protobuf@2.15.0)
      react:
        specifier: ^19.0.0
        version: 19.2.0
    devDependencies:
      vite:
        specifier: ^7.0.0
        version: 7.1.0
packages:
  '@bufbuild/protobuf@2.15.0':
    resolution: {integrity: sha512-AAAA}
  '@connectrpc/connect@2.2.0':
    resolution: {integrity: sha512-AAAA}
  react@19.2.0:
    resolution: {integrity: sha512-AAAA}
  q@1.5.1:
    resolution: {integrity: sha512-AAAA}
  vite@7.1.0:
    resolution: {integrity: sha512-AAAA}
snapshots:
  '@bufbuild/protobuf@2.15.0': {}
  '@connectrpc/connect@2.2.0(@bufbuild/protobuf@2.15.0)':
    dependencies:
      '@bufbuild/protobuf': 2.15.0
  react@19.2.0:
    dependencies:
      '@bufbuild/protobuf': 2.15.0
      q: 1.5.1
  q@1.5.1: {}
  vite@7.1.0: {}
`
	cs, err := sbom.Pnpm([]byte(lock))
	require.NoError(t, err)
	got := map[string]string{}
	for _, c := range cs {
		got[c.PackageURL] = (*c.Hashes)[0].Value
	}
	assert.Equal(t, map[string]string{
		"pkg:npm/%40bufbuild/protobuf@2.15.0": "000000", "pkg:npm/%40connectrpc/connect@2.2.0": "000000", "pkg:npm/react@19.2.0": "000000",
		"pkg:npm/q@1.5.1": "000000",
	}, got)
	for _, bad := range []string{"{", "lockfileVersion: '9.0'\n", "importers:\n  .:\n    dependencies:\n      x:\n        version: nover\n"} {
		_, err := sbom.Pnpm([]byte(bad))
		require.Error(t, err, bad)
	}
}

// Each kind of pin is a component by what holds it: a download by its SHA-256, a GitHub
// repository by its commit, an image by its digest, a module by its version.
func TestPinsAreWhatHoldsThem(t *testing.T) {
	v, err := versions.Parse([]byte(`
- name: qemu
  kind: download
  source: https://download.qemu.org/qemu-{version}.tar.xz
  version: 11.1.2
  pin: 1111111111111111111111111111111111111111111111111111111111111111
  track: tags https://gitlab.com/qemu-project/qemu.git
- name: qboot
  kind: git
  source: https://github.com/bonzini/qboot
  version: master
  pin: 8ca302e86d685fa05b16e2b208888243da319941
  track: branch
- name: e2fsprogs
  kind: git
  source: https://git.kernel.org/pub/scm/fs/ext2/e2fsprogs.git
  version: v1.47.3
  pin: 2222222222222222222222222222222222222222
  archive: 3333333333333333333333333333333333333333333333333333333333333333
  track: tags https://git.kernel.org/pub/scm/fs/ext2/e2fsprogs.git
- name: alpine
  kind: image
  source: alpine
  version: "3.22"
  pin: sha256:4444444444444444444444444444444444444444444444444444444444444444
  track: digest
- name: actionlint
  kind: module
  source: github.com/rhysd/actionlint/cmd/actionlint
  version: v1.7.12
  track: github-release rhysd/actionlint
- name: uefivars
  kind: pypi
  source: uefivars
  version: 1.2.0
  track: pypi
- name: snapshot
  kind: date
  version: 20261001T000000Z
  track: today
`))
	require.NoError(t, err)
	cs, err := sbom.Pins(v, "qemu", "qboot", "e2fsprogs", "alpine", "actionlint", "uefivars", "snapshot")
	require.NoError(t, err)
	purls := map[string]string{}
	for _, c := range cs {
		purls[c.Name] = c.PackageURL
	}
	assert.Equal(t, map[string]string{
		"qemu":       "pkg:generic/qemu@11.1.2?download_url=https://download.qemu.org/qemu-11.1.2.tar.xz",
		"qboot":      "pkg:github/bonzini/qboot@8ca302e86d685fa05b16e2b208888243da319941",
		"e2fsprogs":  "pkg:generic/e2fsprogs@2222222222222222222222222222222222222222?vcs_url=git%2Bhttps://git.kernel.org/pub/scm/fs/ext2/e2fsprogs.git",
		"alpine":     "pkg:oci/alpine@sha256%3A4444444444444444444444444444444444444444444444444444444444444444?repository_url=alpine&tag=3.22",
		"actionlint": "pkg:golang/github.com/rhysd/actionlint/cmd/actionlint@v1.7.12",
		"uefivars":   "pkg:pypi/uefivars@1.2.0",
		"snapshot":   "pkg:generic/snapshot@20261001T000000Z",
	}, purls)
	assert.Equal(t, strings.Repeat("1", 64), (*cs[0].Hashes)[0].Value)
	assert.Equal(t, strings.Repeat("3", 64), (*cs[2].Hashes)[0].Value)
	assert.Nil(t, cs[1].Hashes)
	assert.Equal(t, cdx.ComponentTypeContainer, cs[3].Type)
	// Where each came from: the download's URL, the repository's.
	assert.Equal(t, []cdx.ExternalReference{{Type: cdx.ERTypeDistribution, URL: "https://download.qemu.org/qemu-11.1.2.tar.xz"}}, *cs[0].ExternalReferences)
	assert.Equal(t, []cdx.ExternalReference{{Type: cdx.ERTypeVCS, URL: "https://github.com/bonzini/qboot"}}, *cs[1].ExternalReferences)
	_, err = sbom.Pins(v, "none")
	require.Error(t, err)
}
