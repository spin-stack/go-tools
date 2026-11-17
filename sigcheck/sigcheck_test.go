package sigcheck

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

const (
	ciAtMain      = "https://github.com/spin-stack/ami/.github/workflows/ci.yml@refs/heads/main"
	releaseAtMain = "https://github.com/spin-stack/ami/.github/workflows/release.yml@refs/heads/main"
)

func identity(t *testing.T, san string) verify.CertificateIdentity {
	t.Helper()
	id, err := verify.NewShortCertificateIdentity(Issuer, "", san, "")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// A policy takes a signature by one of its identities over these bytes, and nothing else: another
// workflow, another ref, other bytes, or no identity to hold it to at all.
func TestAPolicyTakesOnlyItsIdentitiesOverTheseBytes(t *testing.T) {
	sigstore, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("statement")
	sum := sha256.Sum256(body)
	for _, tc := range []struct {
		name     string
		signedAs string
		policy   []string
		over     []byte
		// withSCT is the policy as it runs, which asks for a timestamp the test's certificate
		// authority does not give: that it refuses one is the check that it asks.
		withSCT bool
		takes   bool
	}{
		{name: "its identity", signedAs: ciAtMain, policy: []string{ciAtMain}, over: body, takes: true},
		{name: "no certificate timestamp", signedAs: ciAtMain, policy: []string{ciAtMain}, over: body, withSCT: true},
		{name: "one of its identities", signedAs: releaseAtMain, policy: []string{ciAtMain, releaseAtMain}, over: body, takes: true},
		{name: "a branch", signedAs: "https://github.com/spin-stack/ami/.github/workflows/ci.yml@refs/pull/9/merge",
			policy: []string{ciAtMain, releaseAtMain}, over: body},
		{name: "another repository", signedAs: "https://github.com/spin-stack/spin/.github/workflows/ci.yml@refs/heads/main",
			policy: []string{ciAtMain}, over: body},
		{name: "other bytes", signedAs: ciAtMain, policy: []string{ciAtMain}, over: []byte("statemenu")},
		{name: "no identity", signedAs: ciAtMain, over: body},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entity, err := sigstore.Sign(tc.signedAs, Issuer, tc.over)
			if err != nil {
				t.Fatal(err)
			}
			var workflows []string
			for _, san := range tc.policy {
				workflows = append(workflows, strings.TrimSuffix(san, "@refs/heads/main"))
			}
			p, err := WorkflowsUnder(sigstore, "refs/heads/main", workflows...)
			if err != nil {
				t.Fatal(err)
			}
			p.noSCT = !tc.withSCT
			err = p.check(entity, sum[:])
			if tc.takes && err != nil {
				t.Fatal(err)
			}
			if !tc.takes && err == nil {
				t.Fatal("taken")
			}
		})
	}
}

// What is not a bundle is refused before anything is verified.
func TestASignatureThatIsNotABundleIsRefused(t *testing.T) {
	err := Policy{Identities: []verify.CertificateIdentity{identity(t, ciAtMain)}}.Check([]byte("{}"), make([]byte, 32))
	if err == nil || !strings.Contains(err.Error(), "not a bundle") {
		t.Fatalf("err = %v", err)
	}
}

// The policy as it runs reads go-tools' pinned root and names each workflow at the ref.
func TestWorkflowsIsUnderThePinnedRoot(t *testing.T) {
	p, err := Workflows("refs/heads/main", "https://github.com/spin-stack/ami/.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	if p.Trust == nil || len(p.Identities) != 1 || p.noSCT {
		t.Fatalf("policy = %+v", p)
	}
}

// A real signature: spin's release workflow over spin-boot of v20260928.07, under Sigstore's trust
// root as TUF served it then. It passes the policy as it runs - one signed certificate timestamp,
// one log entry and one observed timestamp, which is what a bundle cosign made carries and no
// more - and nothing but that workflow at that tag, over those bytes, passes it.
func TestARealSignaturePassesThePolicyAsItRuns(t *testing.T) {
	trust, err := root.NewTrustedRootFromPath("testdata/trusted_root.json")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := os.ReadFile("testdata/spin-boot-v20260928.07.sigstore.json")
	if err != nil {
		t.Fatal(err)
	}
	spinBoot, err := hex.DecodeString("981491e91956745dc4117a7f6ab3cb8cb03067cddeb89a8c0dea88846ef081a9")
	if err != nil {
		t.Fatal(err)
	}
	const release = "https://github.com/spin-stack/spin/.github/workflows/release.yml"
	for _, tc := range []struct {
		name, workflow, ref string
		sum                 []byte
		passes              bool
	}{
		{name: "the release workflow at its tag", workflow: release, ref: "refs/tags/v20260928.07", sum: spinBoot, passes: true},
		{name: "another tag", workflow: release, ref: "refs/tags/v20260928.08", sum: spinBoot},
		{name: "main", workflow: release, ref: "refs/heads/main", sum: spinBoot},
		{name: "another workflow", workflow: "https://github.com/spin-stack/spin/.github/workflows/ci.yml", ref: "refs/tags/v20260928.07", sum: spinBoot},
		{name: "other bytes", workflow: release, ref: "refs/tags/v20260928.07", sum: make([]byte, 32)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := WorkflowsUnder(trust, tc.ref, tc.workflow)
			if err != nil {
				t.Fatal(err)
			}
			err = p.Check(bundle, tc.sum)
			if tc.passes && err != nil {
				t.Fatal(err)
			}
			if !tc.passes && err == nil {
				t.Fatal("passed")
			}
		})
	}
}
