// Package catalog is the Spin OS image catalog: for each spin release, each role's images by
// region and the builds a machine of it may prove it booted, and the channels that name a
// release. spin-stack/ami writes it on spin-terraform-aws's catalog branch; an installation reads
// it through its Terraform provider.
//
// The catalog is public data that anybody with write access to the branch could change, so
// nothing in it is believed for where it is: a build is believed when its statement is signed by
// the release workflow and says the PCRs the entry says, a revocation and a channel when they are
// signed statements of their own. What a build's PCRs open - the control plane's key, a host's
// join - is decided by that signature, not by who wrote the file.
package catalog

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/spin-stack/go-tools/sigcheck"
)

// Format is the version of the files this package reads and writes.
const Format = 1

// The workflows that sign what the catalog holds, at the one ref whose runs publish: a build's
// statement and a channel's move by the release, a revocation by the workflow that revokes.
const (
	SignerRef        = "refs/heads/main"
	ReleaseWorkflow  = "https://github.com/spin-stack/ami/.github/workflows/release.yml"
	RevokeWorkflow   = "https://github.com/spin-stack/ami/.github/workflows/revoke.yml"
	PromoteWorkflow  = "https://github.com/spin-stack/ami/.github/workflows/promote.yml"
	BranchURL        = "https://raw.githubusercontent.com/spin-stack/spin-terraform-aws/catalog"
	releasesDir      = "releases"
	channelsDir      = "channels"
	statementVersion = 1
)

// Roles are the roles of an installation, each its own image.
var Roles = []string{"control-plane", "proxy", "runner"}

var (
	releaseName = regexp.MustCompile(`^v[0-9]{8}\.[0-9]+$`)
	channelName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	revision    = regexp.MustCompile(`^r[1-9][0-9]*$`)
	pcr         = regexp.MustCompile(`^[0-9a-f]{96}$`)
)

// Signers is what the catalog's signatures are held to, under go-tools' pinned Sigstore root.
func Signers() (sigcheck.Policy, error) {
	return sigcheck.Workflows(SignerRef, ReleaseWorkflow, RevokeWorkflow, PromoteWorkflow)
}

// ReleasePath is where a release's file is on the branch.
func ReleasePath(release string) string { return releasesDir + "/" + release + ".json" }

// ChannelPath is where a channel's file is on the branch.
func ChannelPath(channel string) string { return channelsDir + "/" + channel + ".json" }

// IsRelease says whether name is a release, v<YYYYMMDD>.<N>, rather than a channel.
func IsRelease(name string) bool { return releaseName.MatchString(name) }

// IsChannel says whether name may be a channel's.
func IsChannel(name string) bool { return !IsRelease(name) && channelName.MatchString(name) }

// Release is a release's file: each role's builds, revision by revision, and the revocations of
// some of them.
type Release struct {
	Format  int             `json:"format"`
	Release string          `json:"release"`
	Roles   map[string]Role `json:"roles"`
}

// Role is one role's builds of a release, by revision (r1, r2, ...), and the signed revocations
// of some of them by the same revision.
type Role struct {
	Builds      map[string]Build  `json:"builds"`
	Revocations map[string]Signed `json:"revocations,omitempty"`
}

// Build is one build of a role's image: the PCRs an instance of it measures, the statement of
// them its release signed and the bundle that signs it, and the image registered in each region -
// which an earlier build, never booted again, may no longer name.
type Build struct {
	PCR4      string            `json:"pcr4"`
	PCR7      string            `json:"pcr7"`
	PCR12     string            `json:"pcr12"`
	Statement []byte            `json:"statement"`
	Bundle    []byte            `json:"bundle"`
	Images    map[string]string `json:"images,omitempty"`
	// Provenance is the disk's SLSA provenance bundle, attested by its release: who built the
	// image, from which commit and of what. Nothing is decided by it; it is there for whoever
	// audits a build, so a reader neither needs it nor believes it unchecked.
	Provenance []byte `json:"provenance_bundle,omitempty"`
}

// Signed is a statement and the bundle that signs it.
type Signed struct {
	Statement []byte `json:"statement"`
	Bundle    []byte `json:"bundle"`
}

// Statement is what a build's release signs: ami's images.Statement, as its bytes say it.
type Statement struct {
	FormatVersion int    `json:"format_version"`
	Release       string `json:"release"`
	Role          string `json:"role"`
	Commit        string `json:"commit"`
	RootHash      string `json:"root_hash"`
	Measurements  struct {
		PCR4  string `json:"pcr4"`
		PCR7  string `json:"pcr7"`
		PCR12 string `json:"pcr12"`
	} `json:"measurements"`
}

// Revocation is what a revocation signs: one build of one role of one release is not to be run,
// and why.
type Revocation struct {
	FormatVersion int    `json:"format_version"`
	Release       string `json:"release"`
	Role          string `json:"role"`
	Revision      string `json:"revision"`
	Reason        string `json:"reason"`
	At            string `json:"at"`
}

// Channel is a channel's file: the signed statement of the release it names.
type Channel struct {
	Format int    `json:"format"`
	Signed Signed `json:"signed"`
}

// ChannelStatement is what moving a channel signs.
type ChannelStatement struct {
	FormatVersion int    `json:"format_version"`
	Channel       string `json:"channel"`
	Release       string `json:"release"`
	At            string `json:"at"`
}

// Verified is a release as Verify believes it: each role's builds a machine may prove it booted,
// oldest first, and the newest build, the one an installation boots.
type Verified struct {
	Release string
	Roles   map[string]VerifiedRole
}

// VerifiedRole is one role of a Verified release.
type VerifiedRole struct {
	// Trusted is every build not revoked, oldest first: a machine of an earlier build still
	// running when a later one is published goes on until it is replaced.
	Trusted []VerifiedBuild
	// Newest is the build an installation boots; Revoked, when set, is why it may not be.
	Newest  VerifiedBuild
	Revoked string
}

// VerifiedBuild is a build whose statement its release signed, with its revision.
type VerifiedBuild struct {
	Revision string
	Build
}

// Verify is raw, the file of release, as check believes it. Every build is signed by its release
// and says the PCRs its statement says; a revocation is believed only signed; every role has a
// build. Anything else refuses the whole file: a catalog half believed is one whose other half an
// installation would boot.
func Verify(raw []byte, release string, check sigcheck.Checker) (Verified, error) {
	if !IsRelease(release) {
		return Verified{}, fmt.Errorf("catalog: %q is not a release, v<YYYYMMDD>.<N>", release)
	}
	var r Release
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		return Verified{}, fmt.Errorf("catalog: %s: %w", release, err)
	}
	if r.Format != Format {
		return Verified{}, fmt.Errorf("catalog: %s is of format %d, and %d is read", release, r.Format, Format)
	}
	if r.Release != release {
		return Verified{}, fmt.Errorf("catalog: the file of %s says it is %q", release, r.Release)
	}
	v := Verified{Release: release, Roles: map[string]VerifiedRole{}}
	for name := range r.Roles {
		if !slices.Contains(Roles, name) {
			return Verified{}, fmt.Errorf("catalog: %s names a role %q, which is none of %v", release, name, Roles)
		}
	}
	var errs []error
	for _, name := range Roles {
		role, ok := r.Roles[name]
		if !ok || len(role.Builds) == 0 {
			errs = append(errs, fmt.Errorf("%s has no build of the %s", release, name))
			continue
		}
		vr, err := verifyRole(release, name, role, check)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		v.Roles[name] = vr
	}
	if err := errors.Join(errs...); err != nil {
		return Verified{}, fmt.Errorf("catalog: %w", err)
	}
	return v, nil
}

func verifyRole(release, role string, r Role, check sigcheck.Checker) (VerifiedRole, error) {
	revisions := make([]string, 0, len(r.Builds))
	for rev := range r.Builds {
		if !revision.MatchString(rev) {
			return VerifiedRole{}, fmt.Errorf("the %s of %s has a build %q, which is no revision r<N>", role, release, rev)
		}
		revisions = append(revisions, rev)
	}
	slices.SortFunc(revisions, func(a, b string) int { return revisionNumber(a) - revisionNumber(b) })
	revoked := map[string]string{}
	for rev, s := range r.Revocations {
		var rv Revocation
		if err := verifySigned(s, check, &rv); err != nil {
			return VerifiedRole{}, fmt.Errorf("the revocation of %s of the %s of %s: %w", rev, role, release, err)
		}
		if rv.FormatVersion != statementVersion || rv.Release != release || rv.Role != role || rv.Revision != rev || rv.Reason == "" {
			return VerifiedRole{}, fmt.Errorf("the revocation filed under %s of the %s of %s says it is of %s of the %s of %s", rev, role, release, rv.Revision, rv.Role, rv.Release)
		}
		if _, ok := r.Builds[rev]; !ok {
			return VerifiedRole{}, fmt.Errorf("the %s of %s revokes %s, which it has no build of", role, release, rev)
		}
		revoked[rev] = rv.Reason
	}
	var out VerifiedRole
	for _, rev := range revisions {
		b := r.Builds[rev]
		if err := verifyBuild(release, role, rev, b, check); err != nil {
			return VerifiedRole{}, err
		}
		vb := VerifiedBuild{Revision: rev, Build: b}
		if _, gone := revoked[rev]; !gone {
			out.Trusted = append(out.Trusted, vb)
		}
		out.Newest = vb
	}
	// Only the newest build is booted, so only its images must be known: an earlier build's PCRs
	// are trusted for the machines still running it, and its image may be gone.
	if len(out.Newest.Images) == 0 {
		return VerifiedRole{}, fmt.Errorf("%s of the %s of %s, its newest build, is registered in no region", out.Newest.Revision, role, release)
	}
	out.Revoked = revoked[out.Newest.Revision]
	return out, nil
}

func verifyBuild(release, role, rev string, b Build, check sigcheck.Checker) error {
	for _, p := range []string{b.PCR4, b.PCR7, b.PCR12} {
		if !pcr.MatchString(p) {
			return fmt.Errorf("%s of the %s of %s has a PCR %q that is not SHA-384 in lowercase hex", rev, role, release, p)
		}
	}
	var s Statement
	if err := verifySigned(Signed{Statement: b.Statement, Bundle: b.Bundle}, check, &s); err != nil {
		return fmt.Errorf("%s of the %s of %s: %w", rev, role, release, err)
	}
	if s.FormatVersion != statementVersion || s.Release != release || s.Role != role {
		return fmt.Errorf("%s of the %s of %s is signed as the %s of %s", rev, role, release, s.Role, s.Release)
	}
	if s.Measurements.PCR4 != b.PCR4 || s.Measurements.PCR7 != b.PCR7 || s.Measurements.PCR12 != b.PCR12 {
		return fmt.Errorf("%s of the %s of %s says other PCRs than its signed statement", rev, role, release)
	}
	return nil
}

// VerifyChannel is the release raw, channel's file, names, as check believes it.
func VerifyChannel(raw []byte, channel string, check sigcheck.Checker) (string, error) {
	if !IsChannel(channel) {
		return "", fmt.Errorf("catalog: %q is not a channel's name", channel)
	}
	var c Channel
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return "", fmt.Errorf("catalog: channel %s: %w", channel, err)
	}
	if c.Format != Format {
		return "", fmt.Errorf("catalog: channel %s is of format %d, and %d is read", channel, c.Format, Format)
	}
	var s ChannelStatement
	if err := verifySigned(c.Signed, check, &s); err != nil {
		return "", fmt.Errorf("catalog: channel %s: %w", channel, err)
	}
	if s.FormatVersion != statementVersion || s.Channel != channel || !IsRelease(s.Release) {
		return "", fmt.Errorf("catalog: the file of channel %s is signed as channel %q naming %q", channel, s.Channel, s.Release)
	}
	return s.Release, nil
}

// verifySigned holds s's bundle to check over its statement's bytes, and reads the statement into
// the value given only once it is believed.
func verifySigned(s Signed, check sigcheck.Checker, into any) error {
	if len(s.Statement) == 0 || len(s.Bundle) == 0 {
		return errors.New("it is not signed")
	}
	sum := sha256.Sum256(s.Statement)
	if err := check.Check(s.Bundle, sum[:]); err != nil {
		return fmt.Errorf("its signature: %w", err)
	}
	d := json.NewDecoder(strings.NewReader(string(s.Statement)))
	d.DisallowUnknownFields()
	if err := d.Decode(into); err != nil {
		return fmt.Errorf("its statement: %w", err)
	}
	return nil
}

func revisionNumber(rev string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(rev, "r")) //nolint:errcheck // a revision is held to r<N> before it is sorted
	return n
}
