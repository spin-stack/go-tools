package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/trustroot"
)

// -check is 0 only when TUF serves the pinned root byte for byte, 1 when it serves another, and 2
// when there is no answer to compare: a fetch that failed, or bytes that are no trusted root.
func TestCheckFailsOnARootThatMoved(t *testing.T) {
	pinned := trustroot.JSON()
	moved := bytes.Replace(pinned, []byte(`"https://rekor.sigstore.dev"`), []byte(`"https://rekor2.sigstore.dev"`), 1)
	require.NotEqual(t, pinned, moved)
	for name, tc := range map[string]struct {
		fetch  fetcher
		args   []string
		status int
	}{
		"the same":           {fetch: serving(pinned, nil), args: []string{"-check"}, status: 0},
		"moved":              {fetch: serving(moved, nil), args: []string{"-check"}, status: 1},
		"no answer":          {fetch: serving(nil, errors.New("offline")), args: []string{"-check"}, status: 2},
		"not a trusted root": {fetch: serving([]byte(`{"mediaType":"x"`), nil), args: []string{"-check"}, status: 2},
		"neither":            {fetch: serving(pinned, nil), status: 2},
		"both":               {fetch: serving(pinned, nil), args: []string{"-check", "-write"}, status: 2},
		"an unknown flag":    {fetch: serving(pinned, nil), args: []string{"-renew"}, status: 2},
	} {
		t.Run(name, func(t *testing.T) {
			var out, errs bytes.Buffer
			assert.Equal(t, tc.status, run(tc.fetch, tc.args, &out, &errs), out.String()+errs.String())
		})
	}
}

// -write puts what TUF served in the file, as served; a root it could not parse is not written.
func TestWriteRenewsTheFileAsServed(t *testing.T) {
	moved := bytes.Replace(trustroot.JSON(), []byte(`"https://rekor.sigstore.dev"`), []byte(`"https://rekor2.sigstore.dev"`), 1)
	for name, tc := range map[string]struct {
		served []byte
		status int
		file   []byte
	}{
		"a trusted root":     {served: moved, status: 0, file: moved},
		"not a trusted root": {served: []byte(`{`), status: 2, file: []byte("old")},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, filepath.Dir(trustroot.File)), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, trustroot.File), []byte("old"), 0o600))
			t.Chdir(dir)
			var out, errs bytes.Buffer
			require.Equal(t, tc.status, run(serving(tc.served, nil), []string{"-write"}, &out, &errs), errs.String())
			got, err := os.ReadFile(trustroot.File)
			require.NoError(t, err)
			assert.Equal(t, tc.file, got)
		})
	}
}

// A file it cannot write is no renewal.
func TestWriteThatFailsIsNoAnswer(t *testing.T) {
	t.Chdir(t.TempDir())
	var out, errs bytes.Buffer
	assert.Equal(t, 2, run(serving(trustroot.JSON(), nil), []string{"-write"}, &out, &errs))
}

func serving(b []byte, err error) fetcher {
	return func() ([]byte, error) { return b, err }
}
