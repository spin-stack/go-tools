package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// files is 0 when every file is attested, 1 when one is not, 2 when it was asked nothing it can
// answer.
func TestFilesSaysWhatIsUnattested(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a", "a.provenance.sigstore.json", "checksums.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, f), nil, 0o600))
	}
	for name, tc := range map[string]struct {
		args   []string
		status int
	}{
		"attested":           {args: []string{"files", "-require", "provenance", "-except", "checksums.txt", dir}, status: 0},
		"one is not":         {args: []string{"files", "-require", "provenance", dir}, status: 1},
		"two kinds":          {args: []string{"files", "-require", "provenance,cdx", "-except", "checksums.txt", dir}, status: 1},
		"no subcommand":      {status: 2},
		"another subcommand": {args: []string{"workflows"}, status: 2},
		"no -require":        {args: []string{"files", dir}, status: 2},
		"no directory":       {args: []string{"files", "-require", "provenance"}, status: 2},
		"a directory absent": {args: []string{"files", "-require", "provenance", filepath.Join(dir, "none")}, status: 2},
		"a bad pattern":      {args: []string{"files", "-require", "provenance", "-except", "[", dir}, status: 2},
		"an unknown flag":    {args: []string{"files", "-all", dir}, status: 2},
	} {
		t.Run(name, func(t *testing.T) {
			var out, errs bytes.Buffer
			assert.Equal(t, tc.status, run(tc.args, &out, &errs), out.String()+errs.String())
		})
	}
}
