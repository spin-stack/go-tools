// Package toolcache puts a released program where a command can run it: the archive versions.yaml
// pins for this platform, downloaded, held to its sha256, and unpacked once into a directory named
// by that sha256, so a second run, and another repository that pins the same file, finds it there.
//
// A directory there is complete or absent: an archive is unpacked beside it and renamed into
// place, which is also what makes two processes installing the same pin at once both right.
package toolcache

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
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
	unpack, ok := unpackers[path.Ext(e.URL())]
	if !ok {
		return "", fmt.Errorf("toolcache: %s is %s, and only a .tar.gz or a .zip is unpacked", e.Name, e.URL())
	}
	dir := filepath.Join(root, e.Name, e.Pin)
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return "", fmt.Errorf("toolcache: %w", err)
	}
	archive, err := os.CreateTemp(filepath.Dir(dir), e.Pin+".*.archive")
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
	if got, err := fetch.FileSum(archive.Name()); err != nil || got != e.Pin {
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
//
// since is the release the caller is of, "" when it is of none: a name recorded that is before it
// is not the latest however lately it was asked, because the caller is proof of a later one, and
// is asked again. Without it a command taken from a release minutes old would run for ten minutes
// beside the release before, which has nothing that command and its release added. Releases are
// in the order of their names, as v<YYYYMMDD>.<NN> is.
//
// GitHub's own answer can be before since too: for a minute or two after a release is published,
// /releases/latest still redirects some requests to the one before. That answer is asked again,
// and when it stays it is an error and no release is run: the tasks of the release before are not
// the ones the caller's Taskfile was written for.
func Latest(ctx context.Context, base, name, root, since string) (string, error) {
	return latest(ctx, base, name, root, since, runtime.GOOS, runtime.GOARCH, time.Now(), sleep)
}

// sleep waits d, or less when ctx ends first.
func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func latest(ctx context.Context, base, name, root, since, goos, goarch string, now time.Time, wait func(context.Context, time.Duration)) (string, error) {
	tag, err := latestTag(ctx, base, filepath.Join(root, name, "latest"), since, now, wait)
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
	sums, err := fetch.Bytes(ctx, base+"/releases/download/"+tag+"/"+Checksums, 1<<20) // mutate-exempt: a bound, not a size: a release's checksums are four lines
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

// again is how long each further question waits when GitHub's answer is a release before the
// caller's: seconds, because a person is waiting on the command, and fourteen of them in all,
// after which it is theirs to run again.
var again = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}

// latestTag is the name of base's latest release: the one stamp records when it was written less
// than fresh ago and is not before since, else asked of GitHub (askLatest) and recorded; the one
// stamp records, however old, when GitHub cannot answer.
// An answer before since is not the latest and is not recorded: it is asked again after each of
// again, and is an error when it is still the answer.
func latestTag(ctx context.Context, base, stamp, since string, now time.Time, wait func(context.Context, time.Duration)) (string, error) {
	raw, _ := os.ReadFile(stamp) //nolint:gosec // this cache's own file
	recorded := string(raw)
	if st, err := os.Stat(stamp); recorded != "" && recorded >= since && err == nil && now.Sub(st.ModTime()) < fresh {
		return recorded, nil
	}
	tag, err := askLatest(ctx, base)
	for _, d := range again {
		if err != nil || tag >= since {
			break
		}
		wait(ctx, d)
		tag, err = askLatest(ctx, base)
	}
	if err != nil {
		if recorded != "" {
			return recorded, nil
		}
		return "", fmt.Errorf("toolcache: the latest release of %s: %w", base, err)
	}
	if tag < since {
		return "", fmt.Errorf("toolcache: %s, the release this command is of, is not visible yet: %s/releases/latest still answers %s, "+
			"the release before it, as GitHub does to some requests for a minute or two after a release is published. "+
			"Nothing was run, since what would have run is from before this command: run it again", since, base, tag)
	}
	if err := os.MkdirAll(filepath.Dir(stamp), 0o750); err != nil {
		return "", fmt.Errorf("toolcache: %w", err)
	}
	if err := os.WriteFile(stamp, []byte(tag), 0o600); err != nil {
		return "", fmt.Errorf("toolcache: %w", err)
	}
	return tag, nil
}

// askLatest is the tag base's /releases/latest redirects to or, when it answers anything else, the
// tag GitHub's API names as base's latest release. The redirect is asked first because no API quota
// counts it. The API is asked after because GitHub answered that page 504 for hours on 2026-11-27
// while the API answered, and every go tool task failed; it is asked with GITHUB_TOKEN or GH_TOKEN
// when there is one, as a download is, so a workflow spends its token's quota, not its address's.
func askLatest(ctx context.Context, base string) (string, error) {
	tag, err := redirected(ctx, base)
	repo, onGitHub := strings.CutPrefix(base, "https://github.com/")
	if err == nil || !onGitHub {
		return tag, err
	}
	api := "https://api.github.com/repos/" + repo + "/releases/latest"
	var rel struct {
		Tag string `json:"tag_name"`
	}
	if aerr := fetch.Read(ctx, api, func(body io.Reader) error { return json.NewDecoder(body).Decode(&rel) }); aerr != nil {
		return "", errors.Join(err, aerr)
	}
	if rel.Tag == "" {
		return "", errors.Join(err, fmt.Errorf("%s names no release", api))
	}
	return rel.Tag, nil
}

func redirected(ctx context.Context, base string) (string, error) {
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

// unpackers write an archive into a directory, by the extension of the file's name: upstream
// releases Windows' programs as .zip and the others as .tar.gz.
var unpackers = map[string]func(archive, dir string) error{".gz": untar, ".zip": unzip}

// untar writes the regular files and directories of the gzipped tar at archive into dir, each
// with its mode's permission bits. A name that would land outside dir is refused, and a link
// is not followed or made: what the archive holds is programs, and a link is a way out of dir.
func untar(archive, dir string) error {
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

// unzip is untar for a .zip: its regular files and directories, none outside dir, no link.
func unzip(archive, dir string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }() // read-only
	for _, f := range zr.File {
		name := filepath.Clean(f.Name)
		if !filepath.IsLocal(name) {
			return fmt.Errorf("%s would be written outside the directory", f.Name)
		}
		target := filepath.Join(dir, name)
		switch mode := f.Mode(); {
		case mode.IsDir():
			if err := os.MkdirAll(target, 0o750); err != nil {
				return err
			}
		case mode.IsRegular():
			r, err := f.Open()
			if err != nil {
				return err
			}
			err = write(target, r, mode.Perm())
			if err := errors.Join(err, r.Close()); err != nil {
				return err
			}
		}
	}
	return nil
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
