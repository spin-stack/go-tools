// Package trustroot is Sigstore's trusted root, pinned: the Fulcio certificate authorities, the
// Rekor logs, the certificate transparency logs and the timestamp authorities a bundle is checked
// against, as TUF served them when this file was last written. A check made with it asks no
// network, so a machine verifies what it boots with nothing to reach and nothing that can be
// answered differently on the day. It is renewed by writing the file again (go run
// ./cmd/trustroot -write) and taken by each repository with its next go-tools: the pin is go.sum,
// one copy for all of them.
package trustroot

import (
	"bytes"
	_ "embed"
	"fmt"

	"github.com/sigstore/sigstore-go/pkg/root"
)

// File is where the root is, relative to this repository's root: what -write rewrites.
const File = "trustroot/trusted_root.json"

//go:embed trusted_root.json
var trustedRoot []byte

// Load is the pinned root, parsed.
func Load() (*root.TrustedRoot, error) {
	tr, err := root.NewTrustedRootFromJSON(trustedRoot)
	if err != nil {
		return nil, fmt.Errorf("trustroot: the pinned trusted root: %w", err)
	}
	return tr, nil
}

// JSON is the pinned root as TUF served it, byte for byte: what a renewal compares against.
func JSON() []byte { return bytes.Clone(trustedRoot) }
