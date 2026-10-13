package attestgate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BundleSuffix ends every Sigstore bundle's name: a bundle is never itself something to attest.
const BundleSuffix = ".sigstore.json"

// Files is what in dir lacks an attestation: each regular file, but bundles and the ones a
// pattern in except matches, needs <file>.<kind>.sigstore.json beside it for each kind in require
// (provenance, cdx). Run right before an upload, the files that leave are the files attested.
func Files(dir string, require, except []string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("attestgate: %w", err)
	}
	have := map[string]bool{}
	for _, e := range entries {
		have[e.Name()] = true
	}
	var missing []string
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || strings.HasSuffix(name, BundleSuffix) {
			continue
		}
		skip := false
		for _, p := range except {
			ok, err := filepath.Match(p, name)
			if err != nil {
				return nil, fmt.Errorf("attestgate: -except %q: %w", p, err)
			}
			skip = skip || ok
		}
		if skip {
			continue
		}
		for _, kind := range require {
			if b := name + "." + kind + BundleSuffix; !have[b] {
				missing = append(missing, b)
			}
		}
	}
	return missing, nil
}
