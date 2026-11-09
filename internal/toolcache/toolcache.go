// Package toolcache puts a released program where a command can run it: the archive versions.yaml
// pins for this platform, downloaded, held to its sha256, and unpacked once into a directory named
// by that sha256, so a second run, and another repository that pins the same file, finds it there.
//
// A directory there is complete or absent: an archive is unpacked beside it and renamed into
// place, which is also what makes two processes installing the same pin at once both right.
package toolcache

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spin-stack/go-tools/internal/fetch"
	"github.com/spin-stack/go-tools/versions"
)

// Install is the directory e's archive for this platform is unpacked in, under root: there already
// when an earlier run unpacked it, downloaded and unpacked otherwise.
func Install(ctx context.Context, e versions.Entry, root string) (string, error) {
	return install(ctx, e, root, runtime.GOOS, runtime.GOARCH)
}

func install(ctx context.Context, e versions.Entry, root, goos, goarch string) (string, error) {
	if e.Kind != versions.Download {
		return "", fmt.Errorf("toolcache: %s is a %s, not a download", e.Name, e.Kind)
	}
	e, err := e.For(goos, goarch)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(e.URL(), ".tar.gz") {
		return "", fmt.Errorf("toolcache: %s is %s, and only a .tar.gz is unpacked", e.Name, e.URL())
	}
	dir := filepath.Join(root, e.Name, e.Pin)
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return "", fmt.Errorf("toolcache: %w", err)
	}
	archive, err := os.CreateTemp(filepath.Dir(dir), e.Pin+".*.tar.gz")
	if err != nil {
		return "", fmt.Errorf("toolcache: %w", err)
	}
	defer func() { _ = os.Remove(archive.Name()) }() // the archive is not kept: the directory is the cache
	err = fetch.File(ctx, e.URL(), archive)
	if cerr := archive.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("toolcache: %s: %w", e.Name, err)
	}
	if got, err := sum(archive.Name()); err != nil || got != e.Pin {
		return "", errors.Join(err, fmt.Errorf("toolcache: %s is not what versions.yaml pins: its SHA-256 is %s", e.URL(), got))
	}
	part, err := os.MkdirTemp(filepath.Dir(dir), e.Pin+".*.part")
	if err != nil {
		return "", fmt.Errorf("toolcache: %w", err)
	}
	defer func() { _ = os.RemoveAll(part) }() // gone once renamed; otherwise what was cut short
	if err := unpack(archive.Name(), part); err != nil {
		return "", fmt.Errorf("toolcache: %s: %w", e.Name, err)
	}
	renamed := os.Rename(part, dir)
	// Renamed into place here, or put there first by another process installing the same pin,
	// whose directory is this one's: either way it is there.
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}
	return "", fmt.Errorf("toolcache: %w", renamed)
}

// unpack writes the regular files and directories of the gzipped tar at archive into dir, each
// with its mode's permission bits. A name that would land outside dir is refused, and a link
// is not followed or made: what the archive holds is programs, and a link is a way out of dir.
func unpack(archive, dir string) error {
	f, err := os.Open(archive) //nolint:gosec // the archive this package just downloaded and checked
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }() // read-only
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(h.Name)
		if !filepath.IsLocal(name) {
			return fmt.Errorf("%s would be written outside the directory", h.Name)
		}
		target := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := write(target, tr, fs.FileMode(h.Mode).Perm()); err != nil {
				return err
			}
		}
	}
}

func write(target string, r io.Reader, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode) //nolint:gosec // inside the directory unpack checked
	if err != nil {
		return err
	}
	_, err = io.Copy(out, r) //nolint:gosec // an archive this package checked against its pin, not one a stranger sent
	return errors.Join(err, out.Close())
}

func sum(p string) (string, error) {
	f, err := os.Open(p) //nolint:gosec // the archive this package just downloaded
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }() // read-only
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
