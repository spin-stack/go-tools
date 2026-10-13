package trustroot_test

import (
	"encoding/hex"
	"os"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/trustroot"
)

// A real signature - spin's release workflow over spin-boot of v20260928.07 - verifies under the
// pinned root with nothing reached over the network: its certificate chains to a Fulcio
// authority the root lists, its log entry is under a Rekor key it lists, its certificate
// timestamp under a CT log's. Other bytes, or another workflow, do not.
func TestARealSignatureVerifiesOffline(t *testing.T) {
	tr, err := trustroot.Load()
	require.NoError(t, err)
	raw, err := os.ReadFile("testdata/spin-boot-v20260928.07.sigstore.json")
	require.NoError(t, err)
	b := &bundle.Bundle{}
	require.NoError(t, b.UnmarshalJSON(raw))
	spinBoot, err := hex.DecodeString("981491e91956745dc4117a7f6ab3cb8cb03067cddeb89a8c0dea88846ef081a9")
	require.NoError(t, err)

	verifier, err := verify.NewVerifier(tr, verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1),
		verify.WithSignedCertificateTimestamps(1))
	require.NoError(t, err)
	const release = "https://github.com/spin-stack/spin/.github/workflows/release.yml"
	for name, tc := range map[string]struct {
		san      string
		sum      []byte
		verifies bool
	}{
		"the release workflow at its tag": {san: release + "@refs/tags/v20260928.07", sum: spinBoot, verifies: true},
		"another workflow":                {san: "https://github.com/spin-stack/spin/.github/workflows/ci.yml@refs/tags/v20260928.07", sum: spinBoot},
		"other bytes":                     {san: release + "@refs/tags/v20260928.07", sum: make([]byte, 32)},
	} {
		t.Run(name, func(t *testing.T) {
			id, err := verify.NewShortCertificateIdentity("https://token.actions.githubusercontent.com", "", tc.san, "")
			require.NoError(t, err)
			_, err = verifier.Verify(b, verify.NewPolicy(verify.WithArtifactDigest("sha256", tc.sum), verify.WithCertificateIdentity(id)))
			if tc.verifies {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

// JSON is a copy: a caller that writes into what it was given changes nothing Load reads.
func TestJSONIsACopy(t *testing.T) {
	j := trustroot.JSON()
	clear(j)
	_, err := trustroot.Load()
	require.NoError(t, err)
	require.Equal(t, byte('{'), trustroot.JSON()[0])
}
