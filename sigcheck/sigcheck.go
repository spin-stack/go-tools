// Package sigcheck holds a Sigstore bundle to the GitHub Actions workflows that may have signed it:
// a keyless signature over exactly the bytes asked about, by one of the workflow files named, at
// exactly the ref named. A pattern would let another branch or another workflow pass for the one
// asked, so there is none.
package sigcheck

import (
	"fmt"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"

	"github.com/spin-stack/go-tools/trustroot"
)

// Issuer is who issues a GitHub Actions workflow the certificate it signs with.
const Issuer = "https://token.actions.githubusercontent.com"

// Checker says whether a bundle signs the bytes whose SHA-256 is sum. Policy is the one that runs.
type Checker interface {
	Check(bundle, sum []byte) error
}

// Identity is the certificate a workflow's keyless signature carries: that workflow file, at
// exactly ref.
func Identity(workflow, ref string) (verify.CertificateIdentity, error) {
	return verify.NewShortCertificateIdentity(Issuer, "", workflow+"@"+ref, "")
}

// Workflows is what a file one of workflows signed at ref is held to, under Sigstore's trust root
// as go-tools pins it (trustroot): read from nothing over the network.
func Workflows(ref string, workflows ...string) (Policy, error) {
	trust, err := trustroot.Load()
	if err != nil {
		return Policy{}, fmt.Errorf("sigcheck: %w", err)
	}
	return WorkflowsUnder(trust, ref, workflows...)
}

// WorkflowsUnder is Workflows under trust: a test's Sigstore, or a root the caller holds itself.
func WorkflowsUnder(trust root.TrustedMaterial, ref string, workflows ...string) (Policy, error) {
	p := Policy{Trust: trust}
	for _, w := range workflows {
		id, err := Identity(w, ref)
		if err != nil {
			return Policy{}, fmt.Errorf("sigcheck: %w", err)
		}
		p.Identities = append(p.Identities, id)
	}
	return p, nil
}

// Policy is a keyless signature by one of Identities, with one signed certificate timestamp, one
// transparency log entry and one observed timestamp, under Trust.
type Policy struct {
	Trust      root.TrustedMaterial
	Identities []verify.CertificateIdentity

	// noSCT is for a test's Sigstore, whose certificates carry no signed certificate timestamp.
	// Its zero value is the policy, and nothing outside this package can unset it.
	noSCT bool
}

// Check is the bundle read and held to the policy.
func (p Policy) Check(signed, sum []byte) error {
	b := &bundle.Bundle{}
	if err := b.UnmarshalJSON(signed); err != nil {
		return fmt.Errorf("its signature is not a bundle: %w", err)
	}
	return p.check(b, sum)
}

func (p Policy) check(e verify.SignedEntity, sum []byte) error {
	if len(p.Identities) == 0 {
		return fmt.Errorf("no identity to check its signature against")
	}
	opt := []verify.VerifierOption{
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1), // mutate-exempt: cosign's bundle has two - its log entry's time and a timestamp authority's - so one or two takes the same bundles
	}
	if !p.noSCT {
		opt = append(opt, verify.WithSignedCertificateTimestamps(1))
	}
	verifier, err := verify.NewVerifier(p.Trust, opt...)
	if err != nil {
		return err
	}
	opts := make([]verify.PolicyOption, 0, len(p.Identities))
	for _, id := range p.Identities {
		opts = append(opts, verify.WithCertificateIdentity(id))
	}
	if _, err := verifier.Verify(e, verify.NewPolicy(verify.WithArtifactDigest("sha256", sum), opts...)); err != nil {
		return fmt.Errorf("it is not what the workflow signed: %w", err)
	}
	return nil
}
