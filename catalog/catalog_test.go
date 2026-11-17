package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// sums is a test's signer: a bundle is "signed:<sha256 hex>" of the bytes it signs, and the
// checker takes exactly that.
type sums struct{}

func (sums) Check(bundle, sum []byte) error {
	if string(bundle) != "signed:"+hex.EncodeToString(sum) {
		return errors.New("not signed")
	}
	return nil
}

func sign(t *testing.T, v any) Signed {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return Signed{Statement: raw, Bundle: []byte("signed:" + sha(raw))}
}

func pcrOf(c byte) string { return strings.Repeat(string(c), 96) }

func build(t *testing.T, release, role string, c byte) Build {
	t.Helper()
	var s Statement
	s.FormatVersion, s.Release, s.Role, s.Commit, s.RootHash = 1, release, role, strings.Repeat("c", 40), strings.Repeat("d", 64)
	s.Measurements.PCR4, s.Measurements.PCR7, s.Measurements.PCR12 = pcrOf(c), pcrOf('7'), pcrOf('0')
	signed := sign(t, s)
	return Build{PCR4: pcrOf(c), PCR7: pcrOf('7'), PCR12: pcrOf('0'), Statement: signed.Statement, Bundle: signed.Bundle,
		Images: map[string]string{"us-west-2": "ami-0123456789abcdef0"}}
}

const rel = "v20261117.01"

// A release as the release publishes it: every role, the runner built twice.
func published(t *testing.T) Release {
	t.Helper()
	r := Release{Format: Format, Release: rel, Roles: map[string]Role{}}
	for _, role := range Roles {
		r.Roles[role] = Role{Builds: map[string]Build{"r1": build(t, rel, role, 'a')}}
	}
	r.Roles["runner"].Builds["r2"] = build(t, rel, "runner", 'b')
	return r
}

func raw(t *testing.T, r Release) []byte {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A release whose every build its release signed is believed: each role's newest build is the one
// booted, and every build is trusted, oldest first.
func TestASignedReleaseIsBelieved(t *testing.T) {
	v, err := Verify(raw(t, published(t)), rel, sums{})
	if err != nil {
		t.Fatal(err)
	}
	runner := v.Roles["runner"]
	if runner.Newest.Revision != "r2" || len(runner.Trusted) != 2 || runner.Trusted[0].Revision != "r1" || runner.Revoked != "" {
		t.Fatalf("runner = %+v", runner)
	}
	if len(v.Roles) != len(Roles) {
		t.Fatalf("roles = %v", v.Roles)
	}
}

// A build's provenance is kept for an auditor and decides nothing.
func TestAProvenanceIsCarried(t *testing.T) {
	r := published(t)
	b := r.Roles["runner"].Builds["r2"]
	b.Provenance = []byte("bundle")
	r.Roles["runner"].Builds["r2"] = b
	v, err := Verify(raw(t, r), rel, sums{})
	if err != nil {
		t.Fatal(err)
	}
	if string(v.Roles["runner"].Newest.Provenance) != "bundle" {
		t.Fatalf("provenance = %q", v.Roles["runner"].Newest.Provenance)
	}
}

// An earlier build whose image is gone is still trusted: machines of it may still be running.
func TestAnEarlierBuildNeedsNoImage(t *testing.T) {
	r := published(t)
	b := r.Roles["runner"].Builds["r1"]
	b.Images = nil
	r.Roles["runner"].Builds["r1"] = b
	v, err := Verify(raw(t, r), rel, sums{})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Roles["runner"].Trusted) != 2 {
		t.Fatalf("trusted = %v", v.Roles["runner"].Trusted)
	}
}

// r10 is newer than r9: revisions sort by number, not by text.
func TestRevisionsSortByNumber(t *testing.T) {
	r := published(t)
	r.Roles["proxy"].Builds["r9"] = build(t, rel, "proxy", 'e')
	r.Roles["proxy"].Builds["r10"] = build(t, rel, "proxy", 'f')
	v, err := Verify(raw(t, r), rel, sums{})
	if err != nil {
		t.Fatal(err)
	}
	if v.Roles["proxy"].Newest.Revision != "r10" {
		t.Fatalf("newest = %s", v.Roles["proxy"].Newest.Revision)
	}
	// Every one in order, over enough builds that an order other than by number shows.
	r = published(t)
	for n := 2; n <= 24; n++ {
		r.Roles["control-plane"].Builds["r"+strconv.Itoa(n)] = build(t, rel, "control-plane", 'a')
	}
	v, err = Verify(raw(t, r), rel, sums{})
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range v.Roles["control-plane"].Trusted {
		if b.Revision != "r"+strconv.Itoa(i+1) {
			t.Fatalf("build %d is %s", i, b.Revision)
		}
	}
}

// A signed revocation takes its build out of the trusted, and, of the newest, says why it may not
// be installed.
func TestASignedRevocationRemovesItsBuild(t *testing.T) {
	r := published(t)
	runner := r.Roles["runner"]
	runner.Revocations = map[string]Signed{"r2": sign(t, Revocation{FormatVersion: 1, Release: rel, Role: "runner", Revision: "r2", Reason: "CVE-2026-1", At: "2026-11-17"})}
	r.Roles["runner"] = runner
	v, err := Verify(raw(t, r), rel, sums{})
	if err != nil {
		t.Fatal(err)
	}
	got := v.Roles["runner"]
	if got.Revoked != "CVE-2026-1" || len(got.Trusted) != 1 || got.Trusted[0].Revision != "r1" {
		t.Fatalf("runner = %+v", got)
	}
}

// Each way a file could be changed by whoever writes the branch refuses the whole release.
func TestWhatItsReleaseDidNotSignIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(*Release)
		as   string
		says string
	}{
		"another release's file": {as: "v20261117.02", says: `says it is "v20261117.01"`},
		"a channel's name":       {as: "edge", says: "is not a release"},
		"a format not read":      {edit: func(r *Release) { r.Format = 2 }, says: "format 2"},
		"a role missing":         {edit: func(r *Release) { delete(r.Roles, "proxy") }, says: "no build of the proxy"},
		"a role unknown":         {edit: func(r *Release) { r.Roles["miner"] = r.Roles["proxy"] }, says: `role "miner"`},
		"a PCR moved": {edit: func(r *Release) {
			b := r.Roles["control-plane"].Builds["r1"]
			b.PCR4 = pcrOf('e')
			r.Roles["control-plane"].Builds["r1"] = b
		}, says: "other PCRs than its signed statement"},
		"a PCR not hex": {edit: func(r *Release) {
			b := r.Roles["control-plane"].Builds["r1"]
			b.PCR7 = "zz"
			r.Roles["control-plane"].Builds["r1"] = b
		}, says: "not SHA-384"},
		"unsigned": {edit: func(r *Release) {
			b := r.Roles["runner"].Builds["r2"]
			b.Bundle = []byte("signed:00")
			r.Roles["runner"].Builds["r2"] = b
		}, says: "its signature"},
		"no bundle": {edit: func(r *Release) {
			b := r.Roles["runner"].Builds["r2"]
			b.Bundle = nil
			r.Roles["runner"].Builds["r2"] = b
		}, says: "it is not signed"},
		"no statement": {edit: func(r *Release) {
			b := r.Roles["runner"].Builds["r2"]
			b.Statement = nil
			r.Roles["runner"].Builds["r2"] = b
		}, says: "it is not signed"},
		"a role with no build":     {edit: func(r *Release) { r.Roles["proxy"] = Role{Builds: map[string]Build{}} }, says: "no build of the proxy"},
		"another role's statement": {edit: func(r *Release) { r.Roles["runner"].Builds["r1"] = build(t, rel, "proxy", 'a') }, says: "signed as the proxy"},
		"another release's statement": {edit: func(r *Release) {
			r.Roles["runner"].Builds["r1"] = build(t, "v20261001.01", "runner", 'a')
		}, says: "of v20261001.01"},
		"no region for the newest": {edit: func(r *Release) {
			b := r.Roles["runner"].Builds["r2"]
			b.Images = nil
			r.Roles["runner"].Builds["r2"] = b
		}, says: "r2 of the runner of v20261117.01, its newest build, is registered in no region"},
		"a revision that is none": {edit: func(r *Release) { r.Roles["runner"].Builds["latest"] = build(t, rel, "runner", 'a') }, says: "no revision"},
		"an unsigned revocation": {edit: func(r *Release) {
			role := r.Roles["runner"]
			role.Revocations = map[string]Signed{"r2": {Statement: []byte(`{}`), Bundle: []byte("forged")}}
			r.Roles["runner"] = role
		}, says: "the revocation of r2"},
		"a revocation filed under another": {edit: func(r *Release) {
			role := r.Roles["runner"]
			role.Revocations = map[string]Signed{"r2": sign(t, Revocation{FormatVersion: 1, Release: rel, Role: "runner", Revision: "r1", Reason: "x"})}
			r.Roles["runner"] = role
		}, says: "filed under r2"},
		"a revocation of another format": {edit: func(r *Release) {
			role := r.Roles["runner"]
			role.Revocations = map[string]Signed{"r2": sign(t, Revocation{FormatVersion: 2, Release: rel, Role: "runner", Revision: "r2", Reason: "x"})}
			r.Roles["runner"] = role
		}, says: "filed under r2"},
		"a revocation of another release": {edit: func(r *Release) {
			role := r.Roles["runner"]
			role.Revocations = map[string]Signed{"r2": sign(t, Revocation{FormatVersion: 1, Release: "v20261001.01", Role: "runner", Revision: "r2", Reason: "x"})}
			r.Roles["runner"] = role
		}, says: "filed under r2"},
		"a revocation of another role": {edit: func(r *Release) {
			role := r.Roles["runner"]
			role.Revocations = map[string]Signed{"r2": sign(t, Revocation{FormatVersion: 1, Release: rel, Role: "proxy", Revision: "r2", Reason: "x"})}
			r.Roles["runner"] = role
		}, says: "filed under r2"},
		"a revocation with no reason": {edit: func(r *Release) {
			role := r.Roles["runner"]
			role.Revocations = map[string]Signed{"r2": sign(t, Revocation{FormatVersion: 1, Release: rel, Role: "runner", Revision: "r2"})}
			r.Roles["runner"] = role
		}, says: "filed under r2"},
		"a revocation of no build": {edit: func(r *Release) {
			role := r.Roles["proxy"]
			role.Revocations = map[string]Signed{"r5": sign(t, Revocation{FormatVersion: 1, Release: rel, Role: "proxy", Revision: "r5", Reason: "x"})}
			r.Roles["proxy"] = role
		}, says: "no build of"},
	} {
		t.Run(name, func(t *testing.T) {
			r := published(t)
			if tc.edit != nil {
				tc.edit(&r)
			}
			as := rel
			if tc.as != "" {
				as = tc.as
			}
			_, err := Verify(raw(t, r), as, sums{})
			if err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("err = %v, want one that says %q", err, tc.says)
			}
		})
	}
}

// A field the format does not have is refused rather than ignored: a reader that skipped it would
// believe a file that means more than it read.
func TestAFieldNotInTheFormatIsRefused(t *testing.T) {
	b := strings.Replace(string(raw(t, published(t))), `"format":1`, `"format":1,"trust_all":true`, 1)
	if _, err := Verify([]byte(b), rel, sums{}); err == nil || !strings.Contains(err.Error(), "trust_all") {
		t.Fatalf("err = %v", err)
	}
}

func channelFile(t *testing.T, s ChannelStatement) []byte {
	t.Helper()
	b, err := json.Marshal(Channel{Format: Format, Signed: sign(t, s)})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A channel is the release its signed statement names, for that channel alone.
func TestAChannelIsTheReleaseItsStatementNames(t *testing.T) {
	got, err := VerifyChannel(channelFile(t, ChannelStatement{FormatVersion: 1, Channel: "edge", Release: rel, At: "2026-11-17"}), "edge", sums{})
	if err != nil || got != rel {
		t.Fatalf("got %q, %v", got, err)
	}
	for name, tc := range map[string]struct {
		file    []byte
		channel string
		says    string
	}{
		"another channel's": {channelFile(t, ChannelStatement{FormatVersion: 1, Channel: "stable", Release: rel}), "edge", `channel "stable"`},
		"no release":        {channelFile(t, ChannelStatement{FormatVersion: 1, Channel: "edge", Release: "latest"}), "edge", `naming "latest"`},
		"a release's name":  {channelFile(t, ChannelStatement{FormatVersion: 1, Channel: rel, Release: rel}), rel, "not a channel"},
		"unsigned":          {[]byte(`{"format":1,"signed":{"statement":"e30=","bundle":"Zm9yZ2Vk"}}`), "edge", "its signature"},
		"a format not read": {[]byte(`{"format":3,"signed":{}}`), "edge", "format 3"},
		"not the format":    {[]byte(`{"format":1,"release":"v20261117.01"}`), "edge", "release"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyChannel(tc.file, tc.channel, sums{}); err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("err = %v, want one that says %q", err, tc.says)
			}
		})
	}
}

// The branch's paths, and what the runtime policy holds a signature to.
func TestPathsAndSigners(t *testing.T) {
	if ReleasePath(rel) != "releases/v20261117.01.json" || ChannelPath("edge") != "channels/edge.json" {
		t.Fatal(ReleasePath(rel), ChannelPath("edge"))
	}
	p, err := Signers()
	if err != nil || len(p.Identities) != 3 {
		t.Fatalf("signers = %+v, %v", p, err)
	}
	if !IsChannel("edge") || IsChannel(rel) || IsChannel("Edge") || !IsRelease(rel) {
		t.Fatal("names")
	}
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
