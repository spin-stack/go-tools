package sbom

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"

	cdx "github.com/CycloneDX/cyclonedx-go"
	"go.yaml.in/yaml/v3"

	"github.com/spin-stack/go-tools/versions"
)

// File is the file at path as a component named name at version, by its SHA-256: a firmware
// blob, a binary nothing else describes.
func File(name, version, path string) (cdx.Component, error) {
	sum, err := fileSHA256(path)
	if err != nil {
		return cdx.Component{}, err
	}
	return cdx.Component{Type: cdx.ComponentTypeFile, Name: name, Version: version, Hashes: sha(sum)}, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // a file the caller describes
	if err != nil {
		return "", fmt.Errorf("sbom: %w", err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("sbom: %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Go is the Go binary at path as an application at version - a release names its binaries by the
// release, not by the pseudo-version Go stamps a commit with - made of the modules its build info
// lists, each with the go.sum hash it was built from, and the toolchain that built it as a
// component of its own: the standard library is in every binary.
func Go(version, path string) (cdx.Component, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return cdx.Component{}, fmt.Errorf("sbom: %s: %w", path, err)
	}
	sum, err := fileSHA256(path)
	if err != nil {
		return cdx.Component{}, err
	}
	deps := Modules(info)
	return cdx.Component{
		Type: cdx.ComponentTypeApplication, Name: filepath.Base(path), Version: version,
		PackageURL: "pkg:golang/" + info.Main.Path + "@" + version, Hashes: sha(sum), Components: &deps,
	}, nil
}

// Modules is what a Go build info says the binary is made of: the standard library of its
// toolchain, and each module - the replacement where there is one, since that is what was
// compiled - with the go.sum hash it was checked against.
func Modules(info *debug.BuildInfo) []cdx.Component {
	deps := []cdx.Component{{
		Type: cdx.ComponentTypeLibrary, Name: "stdlib", Version: strings.TrimPrefix(info.GoVersion, "go"),
		PackageURL: "pkg:golang/stdlib@" + strings.TrimPrefix(info.GoVersion, "go"),
	}}
	for _, d := range info.Deps {
		if d.Replace != nil {
			d = d.Replace
		}
		c := cdx.Component{Type: cdx.ComponentTypeLibrary, Name: d.Path, Version: d.Version, PackageURL: "pkg:golang/" + d.Path + "@" + d.Version}
		if d.Sum != "" {
			c.Properties = &[]cdx.Property{{Name: "go:sum", Value: d.Sum}}
		}
		deps = append(deps, c)
	}
	return deps
}

// Apko is the packages of an apko.lock.json, each as Wolfi's, by the SHA-256 of its data section,
// which is what apk checks the package against.
func Apko(raw []byte) ([]cdx.Component, error) {
	var lock struct {
		Contents struct {
			Packages []struct {
				Name         string `json:"name"`
				Version      string `json:"version"`
				Architecture string `json:"architecture"`
				Data         struct {
					Checksum string `json:"checksum"`
				} `json:"data"`
			} `json:"packages"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		return nil, fmt.Errorf("sbom: the apko lock: %w", err)
	}
	var out []cdx.Component
	for _, p := range lock.Contents.Packages {
		c := cdx.Component{
			Type: cdx.ComponentTypeLibrary, Name: p.Name, Version: p.Version,
			PackageURL: "pkg:apk/wolfi/" + p.Name + "@" + p.Version + "?arch=" + p.Architecture,
		}
		if b64, ok := strings.CutPrefix(p.Data.Checksum, "sha256-"); ok {
			sum, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return nil, fmt.Errorf("sbom: %s's checksum: %w", p.Name, err)
			}
			c.Hashes = sha(hex.EncodeToString(sum))
		}
		out = append(out, c)
	}
	return out, nil
}

// Dpkg is the packages of a "<package> <version> <architecture>" listing - dpkg-query -W of an
// image's database - as distro's (ubuntu, debian).
func Dpkg(raw []byte, distro string) ([]cdx.Component, error) {
	var out []cdx.Component
	s := bufio.NewScanner(bytes.NewReader(raw))
	for n := 1; s.Scan(); n++ {
		f := strings.Fields(s.Text())
		if len(f) == 0 {
			continue
		}
		if len(f) != 3 {
			return nil, fmt.Errorf("sbom: line %d of the package list is not <package> <version> <architecture>: %q", n, s.Text())
		}
		out = append(out, cdx.Component{
			Type: cdx.ComponentTypeLibrary, Name: f[0], Version: f[1],
			PackageURL: "pkg:deb/" + distro + "/" + f[0] + "@" + f[1] + "?arch=" + f[2],
		})
	}
	return out, s.Err()
}

// Apk is an apk database (/lib/apk/db/installed) as distro's packages: those named in only are
// what the build put into what it ships - the libraries a static binary links - and the rest are
// what built it, formulation. A name in only the database does not have is an error: the list
// has gone out of step with the build.
func Apk(raw []byte, distro string, only []string) (shipped, built []cdx.Component, err error) {
	seen := map[string]bool{}
	for _, block := range strings.Split(string(raw), "\n\n") {
		var name, version, arch string
		for _, line := range strings.Split(block, "\n") {
			k, v, _ := strings.Cut(line, ":")
			switch k {
			case "P":
				name = v
			case "V":
				version = v
			case "A":
				arch = v
			}
		}
		if name == "" {
			continue
		}
		c := cdx.Component{
			Type: cdx.ComponentTypeLibrary, Name: name, Version: version,
			PackageURL: "pkg:apk/" + distro + "/" + name + "@" + version + "?arch=" + arch,
		}
		if slices.Contains(only, name) {
			seen[name] = true
			shipped = append(shipped, c)
		} else {
			built = append(built, c)
		}
	}
	for _, n := range only {
		if !seen[n] {
			return nil, nil, fmt.Errorf("sbom: %s is not in the apk database", n)
		}
	}
	return shipped, built, nil
}

// Pnpm is the packages a pnpm-lock.yaml's root project depends on to run - its dependencies and
// theirs, never its devDependencies - each as npm's, by its integrity's hash.
func Pnpm(raw []byte) ([]cdx.Component, error) {
	var lock struct {
		Importers map[string]struct {
			Dependencies map[string]struct{ Version string } `yaml:"dependencies"`
		} `yaml:"importers"`
		Packages map[string]struct {
			Resolution struct{ Integrity string } `yaml:"resolution"`
		} `yaml:"packages"`
		Snapshots map[string]struct {
			Dependencies map[string]string `yaml:"dependencies"`
		} `yaml:"snapshots"`
	}
	if err := yaml.Unmarshal(raw, &lock); err != nil {
		return nil, fmt.Errorf("sbom: the pnpm lock: %w", err)
	}
	root, ok := lock.Importers["."]
	if !ok {
		return nil, fmt.Errorf("sbom: the pnpm lock has no root project")
	}
	// A snapshot is name@version, with the peers it was resolved against in parentheses after.
	var todo []string
	for name, d := range root.Dependencies {
		todo = append(todo, name+"@"+d.Version)
	}
	seen := map[string]bool{}
	var out []cdx.Component
	for len(todo) > 0 {
		snap := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if seen[snap] {
			continue
		}
		seen[snap] = true
		for name, v := range lock.Snapshots[snap].Dependencies {
			todo = append(todo, name+"@"+v)
		}
		pkg, _, _ := strings.Cut(snap, "(")
		at := strings.LastIndex(pkg, "@")
		if at <= 0 {
			return nil, fmt.Errorf("sbom: %q is not name@version", snap)
		}
		if seen["pkg "+pkg] {
			continue
		}
		seen["pkg "+pkg] = true
		name, version := pkg[:at], pkg[at+1:]
		p, ok := lock.Packages[pkg]
		if !ok {
			return nil, fmt.Errorf("sbom: the pnpm lock resolves %s and has no package of it", pkg)
		}
		c := cdx.Component{Type: cdx.ComponentTypeLibrary, Name: name, Version: version, PackageURL: "pkg:npm/" + purlName(name) + "@" + version}
		if alg, b64, ok := strings.Cut(p.Resolution.Integrity, "-"); ok && alg == "sha512" {
			sum, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return nil, fmt.Errorf("sbom: %s's integrity: %w", pkg, err)
			}
			c.Hashes = &[]cdx.Hash{{Algorithm: cdx.HashAlgoSHA512, Value: hex.EncodeToString(sum)}}
		}
		out = append(out, c)
	}
	return out, nil
}

// purlName is an npm name as a package URL writes it: a scope's @ escaped.
func purlName(name string) string {
	if scoped, ok := strings.CutPrefix(name, "@"); ok {
		return "%40" + scoped
	}
	return name
}

// Patched is c built with the patches at paths applied to its source, in that order: each, its
// text and its name, in c's pedigree, so what was compiled is the upstream and these changes to
// it, and a scanner can tell a patched component from the upstream one.
func Patched(c cdx.Component, paths ...string) (cdx.Component, error) {
	if len(paths) == 0 {
		return c, nil
	}
	var patches []cdx.Patch
	for _, p := range paths {
		raw, err := os.ReadFile(p) //nolint:gosec // a patch the build applied
		if err != nil {
			return cdx.Component{}, fmt.Errorf("sbom: %w", err)
		}
		patches = append(patches, cdx.Patch{Type: cdx.PatchTypeUnofficial,
			Diff: &cdx.Diff{URL: filepath.Base(p), Text: &cdx.AttachedText{ContentType: "text/x-diff", Content: string(raw)}}})
	}
	c.Pedigree = &cdx.Pedigree{Patches: &patches}
	return c, nil
}

// Pins are the versions.yaml entries named, each as what it is: a download by its URL and
// SHA-256, a repository by its commit, an image by its digest, a module by its version.
func Pins(v *versions.Versions, names ...string) ([]cdx.Component, error) {
	var out []cdx.Component
	for _, n := range names {
		e, err := v.Get(n)
		if err != nil {
			return nil, fmt.Errorf("sbom: %w", err)
		}
		out = append(out, Pin(e))
	}
	return out, nil
}

// Pin is one versions.yaml entry as a component.
func Pin(e versions.Entry) cdx.Component {
	c := cdx.Component{Type: cdx.ComponentTypeLibrary, Name: e.Name, Version: e.Version}
	ref := func(url string) *[]cdx.ExternalReference {
		return &[]cdx.ExternalReference{{Type: cdx.ERTypeDistribution, URL: url}}
	}
	switch e.Kind {
	case versions.Download:
		c.PackageURL = "pkg:generic/" + e.Name + "@" + e.Version + "?download_url=" + e.URL()
		c.Hashes, c.ExternalReferences = sha(e.Pin), ref(e.URL())
	case versions.Git:
		if repo, ok := strings.CutPrefix(e.Source, "https://github.com/"); ok {
			c.PackageURL = "pkg:github/" + strings.TrimSuffix(repo, ".git") + "@" + e.Pin
		} else {
			c.PackageURL = "pkg:generic/" + e.Name + "@" + e.Pin + "?vcs_url=git%2B" + e.Source
		}
		c.ExternalReferences = &[]cdx.ExternalReference{{Type: cdx.ERTypeVCS, URL: e.Source}}
		if e.Archive != "" {
			c.Hashes = sha(e.Archive)
		}
	case versions.Image:
		c.Type = cdx.ComponentTypeContainer
		c.PackageURL = "pkg:oci/" + filepath.Base(e.Source) + "@" + strings.Replace(e.Pin, ":", "%3A", 1) + "?repository_url=" + e.Source + "&tag=" + e.Version
		c.Hashes = sha(strings.TrimPrefix(e.Pin, "sha256:"))
	case versions.Module:
		c.PackageURL = "pkg:golang/" + e.Source + "@" + e.Version
	case versions.PyPI:
		c.PackageURL = "pkg:pypi/" + e.Source + "@" + e.Version
	case versions.Date:
		c.Type = cdx.ComponentTypeData
		c.PackageURL = "pkg:generic/" + e.Name + "@" + e.Version
	}
	return c
}
