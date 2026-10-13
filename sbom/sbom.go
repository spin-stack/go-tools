// Package sbom writes the CycloneDX 1.6 software bill of materials of what a spin-stack repository
// ships: each component by its package URL, version and hash, read from what the build itself
// read - a Go binary's build info, a lock file, a package database, versions.yaml - and never
// guessed. The same inputs are the same bytes: components are sorted, the serial number is derived
// from the subject, and the one time in it is SOURCE_DATE_EPOCH's, so a reproduced build carries
// the same SBOM.
package sbom

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	cdx "github.com/CycloneDX/cyclonedx-go"
)

// Version is the CycloneDX specification a BOM is written in.
const Version = cdx.SpecVersion1_6

// New is the BOM of subject: what it is made of, and what built it but is not in it (formulation:
// a compiler, a build tool, a package installed to build and not shipped). epoch is the build's
// time, the zero time for none.
func New(subject cdx.Component, components, formulation []cdx.Component, epoch time.Time) *cdx.BOM {
	b := cdx.NewBOM()
	sort(components)
	sort(formulation)
	b.SerialNumber = serial(subject)
	b.Metadata = &cdx.Metadata{Component: &subject}
	if !epoch.IsZero() {
		b.Metadata.Timestamp = epoch.UTC().Format(time.RFC3339)
	}
	if len(components) > 0 {
		b.Components = &components
	}
	if len(formulation) > 0 {
		b.Formulation = &[]cdx.Formula{{BOMRef: "formulation", Components: &formulation}}
	}
	return b
}

// Epoch is SOURCE_DATE_EPOCH's time, the zero time when it is unset; one that is not a number of
// seconds is an error, never a guess.
func Epoch(getenv func(string) string) (time.Time, error) {
	s := getenv("SOURCE_DATE_EPOCH")
	if s == "" {
		return time.Time{}, nil
	}
	var sec int64
	if _, err := fmt.Sscan(s, &sec); err != nil || fmt.Sprint(sec) != s {
		return time.Time{}, fmt.Errorf("sbom: SOURCE_DATE_EPOCH %q is not a number of seconds", s)
	}
	return time.Unix(sec, 0), nil
}

// serial is a urn:uuid made of the subject: its hashes when it has them, its name and version
// when it has not, so the same subject is the same serial and another is another.
func serial(c cdx.Component) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\n%s\n%s\n", c.Name, c.Version, c.PackageURL)
	if c.Hashes != nil {
		for _, x := range *c.Hashes {
			_, _ = fmt.Fprintf(h, "%s %s\n", x.Algorithm, x.Value)
		}
	}
	u := h.Sum(nil)[:16]
	u[6] = u[6]&0x0f | 0x80 // version 8: custom
	u[8] = u[8]&0x3f | 0x80 // the RFC 9562 variant
	x := hex.EncodeToString(u)
	return "urn:uuid:" + x[:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:]
}

// sort orders components, and the components of each, by package URL then name then version.
func sort(cs []cdx.Component) {
	for i := range cs {
		if cs[i].Components != nil {
			sort(*cs[i].Components)
		}
	}
	slices.SortStableFunc(cs, func(a, b cdx.Component) int {
		if c := strings.Compare(a.PackageURL, b.PackageURL); c != 0 {
			return c
		}
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.Version, b.Version)
	})
}

// Encode writes b as JSON, indented, in Version.
func Encode(w io.Writer, b *cdx.BOM) error {
	return cdx.NewBOMEncoder(w, cdx.BOMFileFormatJSON).SetPretty(true).EncodeVersion(b, Version)
}

// Write is b encoded at path.
func Write(path string, b *cdx.BOM) error {
	f, err := os.Create(path) //nolint:gosec // where the caller writes its SBOM
	if err != nil {
		return fmt.Errorf("sbom: %w", err)
	}
	if err := Encode(f, b); err != nil {
		_ = f.Close() // the encode's error is the one to say
		return fmt.Errorf("sbom: %s: %w", path, err)
	}
	return f.Close()
}

// Read is the BOM at path.
func Read(path string) (*cdx.BOM, error) {
	f, err := os.Open(path) //nolint:gosec // an SBOM the caller names
	if err != nil {
		return nil, fmt.Errorf("sbom: %w", err)
	}
	defer func() { _ = f.Close() }()
	b := &cdx.BOM{}
	if err := cdx.NewBOMDecoder(f, cdx.BOMFileFormatJSON).Decode(b); err != nil {
		return nil, fmt.Errorf("sbom: %s: %w", path, err)
	}
	if b.BOMFormat != cdx.BOMFormat {
		return nil, fmt.Errorf("sbom: %s is not a CycloneDX BOM", path)
	}
	return b, nil
}

// Merge is the BOM of subject made of the subjects of boms, each with what its BOM says it is
// made of nested under it, and their formulations together: a tarball's BOM of the binaries in
// it, an image's of the tarballs it laid.
func Merge(subject cdx.Component, epoch time.Time, boms ...*cdx.BOM) (*cdx.BOM, error) {
	var components, formulation []cdx.Component
	for _, b := range boms {
		if b.Metadata == nil || b.Metadata.Component == nil {
			return nil, errors.New("sbom: a BOM to merge names no subject")
		}
		c := *b.Metadata.Component
		if b.Components != nil {
			nested := slices.Clone(*b.Components)
			c.Components = &nested
		}
		components = append(components, c)
		if b.Formulation != nil {
			for _, f := range *b.Formulation {
				if f.Components != nil {
					formulation = append(formulation, *f.Components...)
				}
			}
		}
	}
	return New(subject, components, formulation, epoch), nil
}

// Check is every component of b - its subject's, what it is made of, nested or not, and its
// formulation's - with a version a person can act on and something to know it by: a package URL
// or a hash. A version that is empty or Go's "(devel)" is one no scanner can match against an
// advisory, and a component known by neither is one nobody could find again.
func Check(b *cdx.BOM) error {
	var errs []error
	var walk func(where string, cs []cdx.Component)
	check := func(where string, c cdx.Component) {
		name := where + c.Name
		switch {
		case c.Name == "":
			errs = append(errs, fmt.Errorf("%s: a component with no name", where))
		case c.Version == "" || c.Version == "(devel)":
			errs = append(errs, fmt.Errorf("%s: version %q", name, c.Version))
		case c.PackageURL == "" && (c.Hashes == nil || len(*c.Hashes) == 0):
			errs = append(errs, fmt.Errorf("%s: no package URL and no hash", name))
		}
		if c.Components != nil {
			walk(name+"/", *c.Components)
		}
	}
	walk = func(where string, cs []cdx.Component) {
		for _, c := range cs {
			check(where, c)
		}
	}
	if b.Metadata == nil || b.Metadata.Component == nil {
		errs = append(errs, errors.New("the BOM names no subject"))
	} else {
		check("", *b.Metadata.Component)
	}
	if b.Components != nil {
		walk("", *b.Components)
	}
	if b.Formulation != nil {
		for _, f := range *b.Formulation {
			if f.Components != nil {
				walk("formulation/", *f.Components)
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("sbom: %w", errors.Join(errs...))
	}
	return nil
}

// sha is a SHA-256 in hex as CycloneDX writes a hash.
func sha(hexSum string) *[]cdx.Hash {
	return &[]cdx.Hash{{Algorithm: cdx.HashAlgoSHA256, Value: hexSum}}
}
