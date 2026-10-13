// Package fulciotest is a Sigstore for tests whose certificates carry what Fulcio writes into a
// GitHub Actions run's: the source repository, its ref, the build's signer and config. sigstore-go's
// VirtualSigstore mints a leaf with the issuer and nothing else, so a verifier that pins the
// source repository - which a reusable builder needs, since any repository may call one and be
// issued its SAN - has no certificate to pass in a test. This one has its own Fulcio CA and keeps
// the VirtualSigstore's Rekor, CT log and timestamp authority.
package fulciotest

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"time"

	"github.com/secure-systems-lab/go-securesystemslib/dsse"
	"github.com/sigstore/rekor/pkg/pki"
	"github.com/sigstore/rekor/pkg/types"
	"github.com/sigstore/rekor/pkg/types/hashedrekord"
	_ "github.com/sigstore/rekor/pkg/types/hashedrekord/v0.0.1" // the version types.NewProposedEntry makes
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/tlog"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"github.com/sigstore/sigstore/pkg/signature"
	sigdsse "github.com/sigstore/sigstore/pkg/signature/dsse"
)

// Sigstore is trusted material for a verifier, and what signs and attests under it.
type Sigstore struct {
	*ca.VirtualSigstore
	fulcio *root.FulcioCertificateAuthority
	key    *ecdsa.PrivateKey
}

// New is a Sigstore whose Fulcio is valid from five hours ago to an hour from now, as the
// VirtualSigstore's is.
func New() (*Sigstore, error) {
	v, err := ca.NewVirtualSigstore()
	if err != nil {
		return nil, fmt.Errorf("fulciotest: %w", err)
	}
	rootCert, rootKey, err := ca.GenerateRootCa()
	if err != nil {
		return nil, fmt.Errorf("fulciotest: %w", err)
	}
	inter, key, err := ca.GenerateFulcioIntermediate(rootCert, rootKey)
	if err != nil {
		return nil, fmt.Errorf("fulciotest: %w", err)
	}
	now := time.Now()
	return &Sigstore{VirtualSigstore: v, key: key, fulcio: &root.FulcioCertificateAuthority{
		Root: rootCert, Intermediates: []*x509.Certificate{inter}, URI: "https://fulciotest.invalid",
		ValidityPeriodStart: now.Add(-5 * time.Hour), ValidityPeriodEnd: now.Add(time.Hour),
	}}, nil
}

// FulcioCertificateAuthorities is this Sigstore's own Fulcio, the one that knows the extensions.
func (s *Sigstore) FulcioCertificateAuthorities() []root.CertificateAuthority {
	return []root.CertificateAuthority{s.fulcio}
}

// The extensions a leaf carries, by their OIDs: the issuer twice, as Fulcio writes it - raw in the
// deprecated OID, DER in the current one - and the rest as DER strings.
func render(e certificate.Extensions) ([]pkix.Extension, error) {
	out := []pkix.Extension{{Id: certificate.OIDIssuer, Value: []byte(e.Issuer)}} //nolint:staticcheck // Fulcio still writes it, and older verifiers read only it
	if e.GithubWorkflowRepository != "" {
		// What cosign's --certificate-github-workflow-repository reads, which Fulcio still writes.
		out = append(out, pkix.Extension{Id: certificate.OIDGitHubWorkflowRepository, Value: []byte(e.GithubWorkflowRepository)}) //nolint:staticcheck // see above
	}
	for _, f := range []struct {
		id  asn1.ObjectIdentifier
		val string
	}{
		{certificate.OIDIssuerV2, e.Issuer},
		{certificate.OIDBuildSignerURI, e.BuildSignerURI},
		{certificate.OIDBuildSignerDigest, e.BuildSignerDigest},
		{certificate.OIDRunnerEnvironment, e.RunnerEnvironment},
		{certificate.OIDSourceRepositoryURI, e.SourceRepositoryURI},
		{certificate.OIDSourceRepositoryDigest, e.SourceRepositoryDigest},
		{certificate.OIDSourceRepositoryRef, e.SourceRepositoryRef},
		{certificate.OIDBuildConfigURI, e.BuildConfigURI},
		{certificate.OIDBuildTrigger, e.BuildTrigger},
	} {
		if f.val == "" {
			continue
		}
		der, err := asn1.MarshalWithParams(f.val, "utf8")
		if err != nil {
			return nil, err // mutate-exempt: a Go string always marshals as a UTF8String
		}
		out = append(out, pkix.Extension{Id: f.id, Value: der})
	}
	return out, nil
}

// leaf is a certificate for san - a URI, as Fulcio's for a workflow is - carrying ext, valid from a
// minute before at to ten minutes after it, and its key.
func (s *Sigstore) leaf(san string, ext certificate.Extensions, at time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	u, err := url.Parse(san)
	if err != nil || u.Scheme == "" {
		return nil, nil, fmt.Errorf("fulciotest: %q is not a URI", san)
	}
	if ext.Issuer == "" {
		return nil, nil, errors.New("fulciotest: a certificate with no issuer")
	}
	extra, err := render(ext)
	if err != nil {
		return nil, nil, fmt.Errorf("fulciotest: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("fulciotest: %w", err) // mutate-exempt: crypto/rand does not fail
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(at.UnixNano()), URIs: []*url.URL{u},
		NotBefore: at.Add(-time.Minute), NotAfter: at.Add(10 * time.Minute),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		ExtraExtensions: extra,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, s.fulcio.Intermediates[0], key.Public(), s.key)
	if err != nil {
		return nil, nil, fmt.Errorf("fulciotest: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("fulciotest: %w", err) // mutate-exempt: what CreateCertificate made parses
	}
	return cert, key, nil
}

// AttestAtTime is an in-toto attestation of body, signed by a certificate for san with ext, logged at
// at, with the log's proof of inclusion when proof.
func (s *Sigstore) AttestAtTime(san string, ext certificate.Extensions, body []byte, at time.Time, proof bool) (verify.SignedEntity, error) {
	cert, key, err := s.leaf(san, ext, at)
	if err != nil {
		return nil, err
	}
	signer, err := signature.LoadECDSASignerVerifier(key, crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("fulciotest: %w", err) // mutate-exempt: a P-256 key always loads
	}
	envSigner, err := dsse.NewEnvelopeSigner(&sigdsse.SignerAdapter{SignatureSigner: signer, Pub: key.Public()})
	if err != nil {
		return nil, fmt.Errorf("fulciotest: %w", err) // mutate-exempt: one signer is always enough
	}
	env, err := envSigner.SignPayload(context.Background(), "application/vnd.in-toto+json", body)
	if err != nil {
		return nil, fmt.Errorf("fulciotest: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(env.Signatures[0].Sig)
	if err != nil {
		return nil, fmt.Errorf("fulciotest: %w", err) // mutate-exempt: dsse writes standard base64
	}
	entry, err := s.GenerateTlogEntry(cert, env, sig, at.Unix(), proof)
	if err != nil {
		return nil, fmt.Errorf("fulciotest: %w", err)
	}
	return &entity{chain: s.chain(cert), envelope: env, entries: []*tlog.Entry{entry}}, nil
}

// SignAtTime is a signature over artifact by a certificate for san with ext, logged at at, as
// cosign sign-blob's is: a hashedrekord entry with the log's promise of inclusion.
func (s *Sigstore) SignAtTime(san string, ext certificate.Extensions, artifact []byte, at time.Time) (verify.SignedEntity, error) {
	cert, key, err := s.leaf(san, ext, at)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(artifact)
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		return nil, fmt.Errorf("fulciotest: %w", err) // mutate-exempt: crypto/rand does not fail
	}
	entry, err := s.hashedRekord(cert, digest[:], sig, at.Unix())
	if err != nil {
		return nil, err
	}
	return &entity{chain: s.chain(cert), message: bundle.NewMessageSignature(digest[:], "SHA2_256", sig), entries: []*tlog.Entry{entry}}, nil
}

func (s *Sigstore) chain(leaf *x509.Certificate) []*x509.Certificate {
	return []*x509.Certificate{leaf, s.fulcio.Intermediates[0], s.fulcio.Root}
}

// hashedRekord is the log's entry of a signature over digest by cert, as the VirtualSigstore's own
// is: canonical, signed by its log, at index 1000.
func (s *Sigstore) hashedRekord(cert *x509.Certificate, digest, sig []byte, at int64) (*tlog.Entry, error) {
	ctx := context.Background()
	pem, err := cryptoutils.MarshalCertificateToPEM(cert)
	if err != nil {
		return nil, fmt.Errorf("fulciotest: %w", err) // mutate-exempt: a parsed certificate always encodes
	}
	proposed, err := types.NewProposedEntry(ctx, hashedrekord.KIND, hashedrekord.New().DefaultVersion(), types.ArtifactProperties{
		PublicKeyBytes: [][]byte{pem}, PKIFormat: string(pki.X509), ArtifactHash: hex.EncodeToString(digest), SignatureBytes: sig,
	})
	if err != nil {
		return nil, fmt.Errorf("fulciotest: %w", err)
	}
	impl, err := types.CreateVersionedEntry(proposed)
	if err != nil {
		return nil, fmt.Errorf("fulciotest: %w", err)
	}
	body, err := types.CanonicalizeEntry(ctx, impl)
	if err != nil {
		return nil, fmt.Errorf("fulciotest: %w", err)
	}
	logID, err := s.RekorLogID()
	if err != nil {
		return nil, fmt.Errorf("fulciotest: %w", err) // mutate-exempt: the VirtualSigstore's key always has an ID
	}
	const index = 1000
	set, err := s.RekorSignPayload(tlog.RekorPayload{LogID: logID, IntegratedTime: at, LogIndex: index, Body: base64.StdEncoding.EncodeToString(body)})
	if err != nil {
		return nil, fmt.Errorf("fulciotest: %w", err)
	}
	raw, err := hex.DecodeString(logID)
	if err != nil {
		return nil, fmt.Errorf("fulciotest: %w", err) // mutate-exempt: a log ID is hex
	}
	return tlog.NewEntry(body, at, index, raw, set, nil) //nolint:staticcheck // the constructor the VirtualSigstore uses for the same entry
}

// entity is a signed thing as a verifier reads it out of a bundle.
type entity struct {
	chain    []*x509.Certificate
	envelope *dsse.Envelope
	message  *bundle.MessageSignature
	entries  []*tlog.Entry
}

func (e *entity) VerificationContent() (verify.VerificationContent, error) {
	return bundle.NewCertificate(e.chain[0]), nil
}

func (e *entity) SignatureContent() (verify.SignatureContent, error) {
	if e.envelope != nil {
		return &bundle.Envelope{Envelope: e.envelope}, nil
	}
	return e.message, nil
}

func (e *entity) HasInclusionPromise() bool { return true }

func (e *entity) HasInclusionProof() bool {
	for _, t := range e.entries {
		if t.HasInclusionProof() {
			return true
		}
	}
	return false
}

func (e *entity) Timestamps() ([][]byte, error) { return nil, nil }

func (e *entity) TlogEntries() ([]*tlog.Entry, error) { return e.entries, nil }

func (e *entity) Version() (string, error) { return "v0.3", nil }
