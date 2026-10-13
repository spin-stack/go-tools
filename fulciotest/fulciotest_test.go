package fulciotest_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/fulciotest"
)

const (
	issuer  = "https://token.actions.githubusercontent.com"
	builder = "https://github.com/spin-stack/spin-machine/.github/workflows/build-release.yml@refs/heads/main"
)

var own = certificate.Extensions{
	Issuer: issuer, SourceRepositoryURI: "https://github.com/spin-stack/spin-machine", SourceRepositoryRef: "refs/heads/main",
	BuildSignerURI: builder, BuildConfigURI: "https://github.com/spin-stack/spin-machine/.github/workflows/release.yml@refs/heads/main",
	GithubWorkflowRepository: "spin-stack/spin-machine",
}

// statement is an in-toto statement over artifact.
func statement(t *testing.T, artifact []byte) []byte {
	t.Helper()
	sum := sha256.Sum256(artifact)
	b, err := json.Marshal(map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"subject":       []map[string]any{{"name": "f", "digest": map[string]string{"sha256": hexOf(sum[:])}}},
		"predicateType": "https://slsa.dev/provenance/v1",
		"predicate":     map[string]any{},
	})
	require.NoError(t, err)
	return b
}

func hexOf(b []byte) string { return hex.EncodeToString(b) }

// What a verifier pinning the source repository and the build's config takes: a signature and an
// attestation whose certificate says so. One from another repository with the builder's SAN - what
// any repository calling a public reusable builder is issued - is refused, and so is one that says
// nothing of its source.
func TestACertificateCarriesWhatFulcioWrites(t *testing.T) {
	s, err := fulciotest.New()
	require.NoError(t, err)
	artifact := []byte("the machine")
	sum := sha256.Sum256(artifact)
	now := time.Now()
	pinned, err := verify.NewShortCertificateIdentity(issuer, "", builder, "")
	require.NoError(t, err)
	pinned.SourceRepositoryURI = own.SourceRepositoryURI
	pinned.BuildConfigURI = own.BuildConfigURI
	verifier, err := verify.NewVerifier(s, verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1))
	require.NoError(t, err)
	check := func(e verify.SignedEntity) error {
		result, err := verifier.Verify(e, verify.NewPolicy(verify.WithArtifactDigest("sha256", sum[:]), verify.WithCertificateIdentity(pinned)))
		if err == nil {
			// What cosign's --certificate-github-workflow-repository reads, beside what the policy did.
			require.Equal(t, own.GithubWorkflowRepository, result.Signature.Certificate.GithubWorkflowRepository)
			require.Equal(t, own.BuildSignerURI, result.Signature.Certificate.BuildSignerURI)
		}
		return err
	}

	other := own
	other.SourceRepositoryURI, other.GithubWorkflowRepository = "https://github.com/someone/fork", "someone/fork"
	none := certificate.Extensions{Issuer: issuer}
	for name, tc := range map[string]struct {
		ext certificate.Extensions
		ok  bool
	}{
		"its own repository":  {ext: own, ok: true},
		"another repository":  {ext: other},
		"nothing of a source": {ext: none},
	} {
		t.Run(name, func(t *testing.T) {
			signed, err := s.SignAtTime(builder, tc.ext, artifact, now)
			require.NoError(t, err)
			attested, err := s.AttestAtTime(builder, tc.ext, statement(t, artifact), now, true)
			require.NoError(t, err)
			require.True(t, attested.HasInclusionProof())
			require.False(t, signed.HasInclusionProof())
			for what, e := range map[string]verify.SignedEntity{"signature": signed, "attestation": attested} {
				if tc.ok {
					require.NoError(t, check(e), what)
				} else {
					require.Error(t, check(e), what)
				}
			}
		})
	}

	// The SAN is what it was asked for; a certificate needs an issuer and a URI to be for.
	signed, err := s.SignAtTime("https://github.com/spin-stack/spin-machine/.github/workflows/release.yml@refs/heads/main", own, artifact, now)
	require.NoError(t, err)
	require.Error(t, check(signed))
	_, err = s.SignAtTime("not a uri", own, artifact, now)
	require.Error(t, err)
	_, err = s.AttestAtTime(builder, certificate.Extensions{}, statement(t, artifact), now, true)
	require.Error(t, err)
	// Signed at a time its certificate was not yet valid, or its Fulcio was not: refused.
	late, err := s.SignAtTime(builder, own, artifact, now.Add(3*time.Hour))
	require.NoError(t, err)
	require.Error(t, check(late))
}
