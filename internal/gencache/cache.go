package gencache

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// unused is how long an entry is kept after it was last used. A key that has not been asked for
// in a week belongs to a tree nobody builds any more, and the directory is carried from run to
// run by CI, which pays for every byte of it.
const unused = 7 * 24 * time.Hour

// cache keeps a mock's bytes under its key, a file each, in one directory: a repository has a
// few hundred mocks, and a week of keys is a few thousand files.
type cache struct{ dir string }

func (c cache) path(key string) string { return filepath.Join(c.dir, key) }

// get is the bytes kept under a key, and marks the entry used.
func (c cache) get(key string) ([]byte, bool) {
	p := c.path(key)
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	now := time.Now()
	_ = os.Chtimes(p, now, now) // an entry not marked is trimmed a week early, and made again
	return b, true
}

func (c cache) put(key string, b []byte) error {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	return writeFile(c.path(key), b)
}

// trim removes the entries no run has used for a week.
func (c cache) trim() error {
	entries, err := os.ReadDir(c.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-unused)
	for _, e := range entries {
		// An entry another run removed between the two calls has nothing left to trim, and one
		// that cannot be removed is a few kilobytes kept a while longer.
		if fi, err := e.Info(); err == nil && fi.ModTime().Before(cutoff) {
			_ = os.Remove(c.path(e.Name()))
		}
	}
	return nil
}
