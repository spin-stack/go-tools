// Package sfw puts every package a repository installs from npm through Socket Firewall Free,
// which refuses a package Socket has confirmed as malware before it is written to disk: Binary is
// the firewall itself, at the pin this module carries, and Check is the gate that fails on an
// install that does not go through it.
package sfw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spin-stack/go-tools/versions"
	"github.com/spin-stack/go-tools/versions/upstream"
)

// limit is past what one release's binary is (each is about 135 MB, a runtime bundled in): a
// server sending more is not sending the file the pin names.
const limit = 512 << 20

// Entry is the pin for the platform goos/goarch, named sfw-<goos>-<goarch>.
func Entry(v *versions.Versions, goos, goarch string) (versions.Entry, error) {
	e, err := v.Get("sfw-" + goos + "-" + goarch)
	if err != nil {
		return versions.Entry{}, fmt.Errorf("sfw: Socket Firewall Free has no pinned binary for %s/%s: %w", goos, goarch, err)
	}
	return e, nil
}

// Binary is the path of e's binary under cache, downloaded the first time and checked against
// its pin every time: what sits in a cache directory is whatever last wrote there, and an
// unsigned binary that every install runs through is the one file not to take on trust.
func Binary(ctx context.Context, e versions.Entry, cache string) (string, error) {
	dir := filepath.Join(cache, e.Pin)
	path := filepath.Join(dir, "sfw")
	switch sum, err := sha256File(path); {
	case err == nil && sum == e.Pin:
		return path, nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("sfw: %w", err)
	}
	raw, err := upstream.Pinned(ctx, e, limit)
	if err != nil {
		return "", fmt.Errorf("sfw: %w", err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("sfw: %w", err)
	}
	// Written beside and renamed over: two installs starting at once each see a whole binary or
	// none, never the other's half.
	tmp, err := os.CreateTemp(dir, "sfw-*")
	if err != nil {
		return "", fmt.Errorf("sfw: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // gone once renamed
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("sfw: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("sfw: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil { //nolint:gosec // a program, run by this user
		return "", fmt.Errorf("sfw: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("sfw: %w", err)
	}
	return path, nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // the cache's own file, under a directory named by its pin
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
