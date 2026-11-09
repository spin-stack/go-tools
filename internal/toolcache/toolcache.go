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
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

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

// Checksums is the file every release of a repository Latest reads lists its archives in, as
// sha256sum writes them.
const Checksums = "checksums.txt"

// fresh is how long a release's name, once asked, is taken as the latest without asking again: a
// Taskfile runs `go tool task` many times a minute, and a new release can wait that long.
const fresh = 10 * time.Minute

// Latest is the directory the latest release of the repository at base (https://github.com/<o>/<r>)
// is unpacked in under root: its archive <name>_<os>_<arch>.tar.gz, held to the sha256 the
// release's checksums file gives it. The name of the latest release is asked at most every ten
// minutes, and a machine that cannot ask goes on with the last one it had.
func Latest(ctx context.Context, base, name, root string) (string, error) {
	return latest(ctx, base, name, root, runtime.GOOS, runtime.GOARCH, time.Now())
}

func latest(ctx context.Context, base, name, root, goos, goarch string, now time.Time) (string, error) {
	tag, err := latestTag(ctx, base, filepath.Join(root, name, "latest"), now)
	if err != nil {
		return "", err
	}
	// A release installed before is its directory, by the sha256 recorded when it was: no network,
	// which is what lets a machine that cannot reach GitHub go on with the release it has.
	installed := filepath.Join(root, name, tag+"."+goos+"_"+goarch)
	if pin, err := os.ReadFile(installed); err == nil { //nolint:gosec // this cache's own file
		if dir := filepath.Join(root, name, string(pin)); exists(dir) {
			return dir, nil
		}
	}
	archive := name + "_" + goos + "_" + goarch + ".tar.gz"
	sums, err := fetch.Bytes(ctx, base+"/releases/download/"+tag+"/"+Checksums, 1<<20)
	if err != nil {
		return "", fmt.Errorf("toolcache: %s %s: %w", name, tag, err)
	}
	pin := ""
	for line := range strings.Lines(string(sums)) {
		if sum, file, ok := strings.Cut(strings.TrimSpace(line), "  "); ok && file == archive {
			pin = sum
		}
	}
	if pin == "" {
		return "", fmt.Errorf("toolcache: %s %s lists no %s", name, tag, archive)
	}
	dir, err := install(ctx, versions.Entry{Name: name, Kind: versions.Download, Version: tag,
		Source: base + "/releases/download/{version}/" + name + "_{os}_{arch}.tar.gz",
		Pins:   map[string]string{goos + "/" + goarch: pin}}, root, goos, goarch)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(installed, []byte(pin), 0o600); err != nil {
		return "", fmt.Errorf("toolcache: %w", err)
	}
	return dir, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// latestTag is the name of base's latest release: the one stamp records when it was written less
// than fresh ago, else asked of GitHub - where /releases/latest redirects to the release, which no
// API quota counts - and recorded; the one stamp records, however old, when GitHub cannot answer.
func latestTag(ctx context.Context, base, stamp string, now time.Time) (string, error) {
	recorded, _ := os.ReadFile(stamp) //nolint:gosec // this cache's own file
	if st, err := os.Stat(stamp); err == nil && len(recorded) > 0 && now.Sub(st.ModTime()) < fresh {
		return string(recorded), nil
	}
	tag, err := askLatest(ctx, base)
	if err != nil {
		if len(recorded) > 0 {
			return string(recorded), nil
		}
		return "", fmt.Errorf("toolcache: the latest release of %s: %w", base, err)
	}
	if err := os.MkdirAll(filepath.Dir(stamp), 0o750); err != nil {
		return "", fmt.Errorf("toolcache: %w", err)
	}
	if err := os.WriteFile(stamp, []byte(tag), 0o600); err != nil {
		return "", fmt.Errorf("toolcache: %w", err)
	}
	return tag, nil
}

// askLatest is the tag base's /releases/latest redirects to.
func askLatest(ctx context.Context, base string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, base+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{
		Transport:     http.DefaultClient.Transport,
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	_ = resp.Body.Close() // a HEAD's: nothing in it
	_, tag, ok := strings.Cut(resp.Header.Get("Location"), "/releases/tag/")
	if resp.StatusCode != http.StatusFound || !ok || tag == "" {
		return "", fmt.Errorf("%s/releases/latest answered %s, not a release", base, resp.Status)
	}
	return tag, nil
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
