package versions

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spin-stack/go-tools/internal/fetch"
)

// errFollows is an entry whose newest version is another's to say: a person reads it there.
var errFollows = errors.New("versions: follows another")

// Newest is the version check says e could be at: the same for an image tracked by its digest,
// or a branch, whose pin is what moves.
func Newest(ctx context.Context, e Entry) (string, error) {
	kind, arg, _ := strings.Cut(e.Track, " ")
	switch kind {
	case "github-release":
		repo := e.Source
		if arg != "" {
			repo = arg
		}
		tag, err := githubRelease(ctx, repo)
		// Spelled as the pin is: a download's URL may add the v its tags have.
		if !strings.HasPrefix(e.Version, "v") {
			tag = strings.TrimPrefix(tag, "v")
		}
		return tag, err
	case "go":
		return newestGo(ctx, e.Version)
	case "kernel-stable":
		return kernelStable(ctx, e.Version)
	case "digest", "branch":
		return e.Version, nil
	case "tags":
		tags, err := releaseTags(ctx, arg, strings.HasPrefix(e.Version, "v"))
		if err != nil {
			return "", err
		}
		if e.Kind != Download {
			return tags[0], nil
		}
		return newestPublished(ctx, e, tags)
	case "commit":
		repo, branch, _ := strings.Cut(arg, " ")
		return branchHead(ctx, repo, branch)
	case "pypi":
		var p struct {
			Info struct {
				Version string `json:"version"`
			} `json:"info"`
		}
		if err := getJSON(ctx, "https://pypi.org/pypi/"+e.Source+"/json", &p); err != nil {
			return "", err
		}
		return p.Info.Version, nil
	case "page":
		return newestOnPage(ctx, arg, e.Source)
	case "today":
		return time.Now().UTC().Format("20060102") + "T000000Z", nil
	case "follows":
		return "", fmt.Errorf("%w: %s", errFollows, arg)
	}
	return "", fmt.Errorf("versions: %s tracks %q, which check does not know", e.Name, e.Track)
}

// Resolve is the pin of e at version: an image's digest, a tag's or a branch's commit, a
// download's sha256.
func Resolve(ctx context.Context, e Entry, version string) (string, error) {
	e.Version = version
	switch e.Kind {
	case Image:
		out, err := run(ctx, "docker", "buildx", "imagetools", "inspect", e.Source+":"+version, "--format", "{{.Manifest.Digest}}")
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(out), nil
	case Git:
		out, err := run(ctx, "git", "ls-remote", e.Source,
			"refs/tags/"+version, "refs/tags/"+version+"^{}", "refs/heads/"+version)
		if err != nil {
			return "", err
		}
		// An annotated tag is its own object: the commit is the line for the tag peeled (^{}).
		pin := ""
		sc := bufio.NewScanner(strings.NewReader(out))
		for sc.Scan() {
			sha, ref, _ := strings.Cut(sc.Text(), "\t")
			if pin == "" || strings.HasSuffix(ref, "^{}") {
				pin = sha
			}
		}
		if pin == "" {
			return "", fmt.Errorf("versions: %s has no tag or branch %s", e.Source, version)
		}
		return pin, nil
	case Download:
		return sha256Of(ctx, e.URL())
	case Date, PyPI:
		return "", nil
	}
	return "", fmt.Errorf("versions: %s is a %s, which nothing resolves", e.Name, e.Kind)
}

// Bump is v's file with name pinned at version - the newest its track finds when version is "" -
// and the entry as that file has it. Nothing is written: that is the caller's.
func (v *Versions) Bump(ctx context.Context, name, version string) (Entry, []byte, error) {
	e, err := v.Get(name)
	if err != nil {
		return Entry{}, nil, err
	}
	// A new commit with the old archive's sum is a build that fails on the checksum, at best.
	if e.Archive != "" {
		return Entry{}, nil, fmt.Errorf("versions: %s's archive is what a build's git writes of the commit, so it is bumped by hand: see its note", name)
	}
	if version == "" {
		if version, err = Newest(ctx, e); err != nil {
			return Entry{}, nil, fmt.Errorf("%w; name the version", err)
		}
	}
	pin, err := Resolve(ctx, e, version)
	if err != nil {
		return Entry{}, nil, err
	}
	out, err := v.Set(name, version, pin)
	if err != nil {
		return Entry{}, nil, err
	}
	e.Version, e.Pin = version, pin
	return e, out, nil
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("versions: %s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

func getJSON(ctx context.Context, url string, into any) error {
	body, err := fetch.Get(ctx, url)
	if err != nil {
		return fmt.Errorf("versions: %w", err)
	}
	defer func() { _ = body.Close() }() // read-only
	return json.NewDecoder(body).Decode(into)
}

func sha256Of(ctx context.Context, url string) (string, error) {
	body, err := fetch.Get(ctx, url)
	if err != nil {
		return "", fmt.Errorf("versions: %w", err)
	}
	defer func() { _ = body.Close() }() // read-only
	h := sha256.New()
	if _, err := io.Copy(h, body); err != nil {
		return "", fmt.Errorf("versions: reading %s: %w", url, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func githubRelease(ctx context.Context, repo string) (string, error) {
	path, ok := strings.CutPrefix(repo, "https://github.com/")
	if !ok {
		return "", fmt.Errorf("versions: %s is not a GitHub repository", repo)
	}
	var release struct {
		Tag string `json:"tag_name"`
	}
	if err := getJSON(ctx, "https://api.github.com/repos/"+path+"/releases/latest", &release); err != nil {
		return "", err
	}
	return release.Tag, nil
}

// release is a tag that names a release and nothing else: v11.1.1, 1.47.4, v27. A -rc, a
// -pre or a WIP tag is not one, and neither is anything with a word in it.
var release = regexp.MustCompile(`^v?(\d+(?:\.\d+)*)$`)

// releaseTags is repo's release tags, highest first, spelled with a v only when the pin is:
// QEMU's tags are v11.1.1 and its tarball is qemu-11.1.1.
func releaseTags(ctx context.Context, repo string, withV bool) ([]string, error) {
	if repo == "" {
		return nil, errors.New("versions: a tags track names no repository")
	}
	out, err := run(ctx, "git", "ls-remote", "--tags", "--refs", repo)
	if err != nil {
		return nil, err
	}
	var found [][]int
	for line := range strings.Lines(out) {
		_, ref, _ := strings.Cut(strings.TrimSpace(line), "\t")
		m := release.FindStringSubmatch(strings.TrimPrefix(ref, "refs/tags/"))
		if m == nil {
			continue
		}
		n, err := numbers(m[1])
		if err != nil {
			return nil, fmt.Errorf("versions: %s's tag %s: %w", repo, ref, err)
		}
		found = append(found, n)
	}
	if found == nil {
		return nil, fmt.Errorf("versions: %s has no release tag", repo)
	}
	slices.SortFunc(found, func(a, b []int) int { return slices.Compare(b, a) })
	tags := make([]string, len(found))
	for i, n := range found {
		parts := make([]string, len(n))
		for j, x := range n {
			parts[j] = strconv.Itoa(x)
		}
		tags[i] = strings.Join(parts, ".")
		if withV {
			tags[i] = "v" + tags[i]
		}
	}
	return tags, nil
}

// newestPublished is the highest of tags, given highest first, whose download is there - and e's
// own version when none newer is.
//
// A tag is not a release. QEMU tagged v11.1.2 before download.qemu.org had its tarball
// (2026-09-28), so check called qemu behind and a bump to the newest failed on a 404. What a
// download entry builds from is the download, so that is what is asked.
func newestPublished(ctx context.Context, e Entry, tags []string) (string, error) {
	current, err := numbers(strings.TrimPrefix(e.Version, "v"))
	if err != nil {
		return "", fmt.Errorf("versions: %s's version %q: %w", e.Name, e.Version, err)
	}
	for _, tag := range tags {
		n, err := numbers(strings.TrimPrefix(tag, "v"))
		if err != nil {
			return "", fmt.Errorf("versions: %s's tag %q: %w", e.Name, tag, err)
		}
		if slices.Compare(n, current) <= 0 {
			break
		}
		c := e
		c.Version = tag
		ok, err := fetch.Exists(ctx, c.URL())
		if err != nil {
			return "", fmt.Errorf("versions: %w", err)
		}
		if ok {
			return tag, nil
		}
	}
	return e.Version, nil
}

// branchHead is the commit branch of repo is at: the newest of a download whose version is a
// commit, from a project whose releases do not carry the file (moby's check-config.sh).
func branchHead(ctx context.Context, repo, branch string) (string, error) {
	if repo == "" || branch == "" {
		return "", errors.New("versions: a commit track is `commit <repo> <branch>`")
	}
	out, err := run(ctx, "git", "ls-remote", repo, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	sha, _, _ := strings.Cut(out, "\t")
	if sha == "" {
		return "", fmt.Errorf("versions: %s has no branch %s", repo, branch)
	}
	return sha, nil
}

// numbers is a dotted version as numbers, so 11.10 comes after 11.9.
func numbers(version string) ([]int, error) {
	var out []int
	for f := range strings.SplitSeq(version, ".") {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// newestOnPage is the version of the first download page links to whose file's name is source's,
// {version} standing for the version: for a publisher that says what is newest nowhere but there.
func newestOnPage(ctx context.Context, page, source string) (string, error) {
	before, after, ok := strings.Cut(path.Base(source), "{version}")
	if !ok {
		return "", fmt.Errorf("versions: %s has no {version} in its file's name", source)
	}
	raw, err := fetch.Bytes(ctx, page, 16<<20)
	if err != nil {
		return "", fmt.Errorf("versions: %w", err)
	}
	link := regexp.MustCompile(regexp.QuoteMeta(before) + `([A-Za-z0-9._-]+)` + regexp.QuoteMeta(after))
	m := link.FindSubmatch(raw)
	if m == nil {
		return "", fmt.Errorf("versions: %s links to no %s", page, path.Base(source))
	}
	return string(m[1]), nil
}

var goTag = regexp.MustCompile(`^(\d+\.\d+(?:\.\d+)?)(.*)$`)

// newestGo is the newest stable Go, spelled as tag is: 1.27.1-trixie stays -trixie.
func newestGo(ctx context.Context, tag string) (string, error) {
	m := goTag.FindStringSubmatch(tag)
	if m == nil {
		return "", fmt.Errorf("versions: %q is not a Go image tag", tag)
	}
	var releases []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
	}
	if err := getJSON(ctx, "https://go.dev/dl/?mode=json", &releases); err != nil {
		return "", err
	}
	for _, r := range releases {
		if r.Stable {
			return strings.TrimPrefix(r.Version, "go") + m[2], nil
		}
	}
	return "", errors.New("versions: go.dev lists no stable Go")
}

// kernelStable is the newest release of version's series, 7.2 for 7.2.8.
func kernelStable(ctx context.Context, version string) (string, error) {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return "", fmt.Errorf("versions: %q is not a kernel release", version)
	}
	series := parts[0] + "." + parts[1] + "."
	var doc struct {
		Releases []struct {
			Version string `json:"version"`
			Moniker string `json:"moniker"`
		} `json:"releases"`
	}
	if err := getJSON(ctx, "https://www.kernel.org/releases.json", &doc); err != nil {
		return "", err
	}
	for _, r := range doc.Releases {
		if r.Moniker != "mainline" && strings.HasPrefix(r.Version, series) {
			return r.Version, nil
		}
	}
	return "", fmt.Errorf("versions: kernel.org lists no %sx: the series is over, and moving to another is a decision, not a bump", series)
}

// Status is one entry as check finds it.
type Status struct {
	Entry  Entry
	Newest string
	// Behind is a newer version, or the same one at another pin.
	Behind bool
	Note   string
}

// Check is where each entry stands against its upstream. An image tracked by its digest, or a
// commit tracked by its branch, is behind when the name now points at something else; a download
// only when its version moves, since its pin is what that version was published as.
func Check(ctx context.Context, v *Versions) []Status {
	var out []Status
	for _, e := range v.Entries {
		st := Status{Entry: e}
		newest, err := Newest(ctx, e)
		switch {
		case errors.Is(err, errFollows):
			st.Note = "follows " + strings.TrimPrefix(e.Track, "follows ") + ": " + e.Note
		case err != nil:
			st.Note = err.Error()
		default:
			st.Newest = newest
			st.Behind = newest != e.Version
			// A file pinned to a commit of a busy branch: the branch moves several times a day and
			// the file almost never. Behind is the file changing, not the commit.
			if st.Behind && e.Kind == Download && strings.HasPrefix(e.Track, "commit ") {
				pin, err := Resolve(ctx, e, newest)
				switch {
				case err != nil:
					st.Note = err.Error()
				case pin == e.Pin:
					st.Behind, st.Note = false, "the branch moved; the file did not"
				}
			}
			if !st.Behind && (e.Kind == Image || e.Kind == Git) {
				pin, err := Resolve(ctx, e, newest)
				if err != nil {
					st.Note = err.Error()
				} else if pin != e.Pin {
					st.Behind, st.Note = true, e.Version+" is now "+pin
				}
			}
		}
		out = append(out, st)
	}
	return out
}
