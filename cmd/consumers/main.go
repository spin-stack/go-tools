// Command consumers says which commit of a repository each place that copies from it is on. For
// every consumer in the repository's consumers.toml (package consumers documents the table) it
// reads that repository's pin and its copies at its default branch, and prints the commit and the
// release it is on, how far behind main that is, whether each copy is what this repository made at
// that commit, and whether the npm packages it installs are the ones versions.yaml pinned there.
//
//	go tool task consumers
//	go tool task consumers -- -checkout spin-stack/spin=../spin -checkout spin-stack/t3code=../t3code@fork/spin
//
// A consumer is read through `gh api`, or from a checkout given by -checkout (at origin/HEAD, or
// the ref after the @). This repository is read from the clone it runs in, which has to hold the
// commits the consumers pin: a shallow clone does not.
//
// It is a report and exits 0 whatever it finds: a consumer that is behind is that repository's to
// fix, and this one's gate must not fail for it. A repository that cannot be read is said so,
// and the others are still reported.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/spin-stack/go-tools/consumers"
	"github.com/spin-stack/go-tools/internal/interrupt"
	"github.com/spin-stack/go-tools/internal/release"
)

// source is a consumer's repository at its default branch.
type source interface {
	// name is what answered - GitHub's own name for the repository, a checkout and its ref - or
	// why nothing of it can be read.
	name(ctx context.Context) (string, error)
	// read is a file's bytes, and false when the repository has no such file.
	read(ctx context.Context, path string) ([]byte, bool, error)
}

type github struct{ repo string }

func (g github) name(ctx context.Context) (string, error) {
	out, err := output(ctx, "gh", "api", "repos/"+g.repo, "--jq", ".full_name")
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return "", errors.New("private or missing: GitHub answers 404 to this login")
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (g github) read(ctx context.Context, path string) ([]byte, bool, error) {
	out, err := output(ctx, "gh", "api", "-H", "Accept: application/vnd.github.raw", "repos/"+g.repo+"/contents/"+path)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return nil, false, nil
		}
		return nil, false, err
	}
	return out, true, nil
}

type checkout struct{ dir, ref string }

func (c checkout) name(ctx context.Context) (string, error) {
	if _, err := output(ctx, "git", "-C", c.dir, "rev-parse", "--verify", "--quiet", c.ref+"^{commit}"); err != nil {
		return "", fmt.Errorf("%s has no %s", c.dir, c.ref)
	}
	return c.dir + " at " + c.ref, nil
}

// read answers false for any path git cannot show: name has already said the ref is a commit.
func (c checkout) read(ctx context.Context, path string) ([]byte, bool, error) {
	out, err := output(ctx, "git", "-C", c.dir, "show", c.ref+":"+path)
	return out, err == nil, nil
}

// producer is this repository: what its consumers call it, a clone of it, and the ref a consumer
// is behind.
type producer struct{ name, dir, main string }

func (p producer) git(ctx context.Context, args ...string) ([]byte, error) {
	return output(ctx, "git", append([]string{"-C", p.dir}, args...)...)
}

// releases is the tags git lists with args that are releases' names, oldest first: a repository
// tags other things too - brand its screens' archives - and none of them is a release a consumer
// can be on.
func (p producer) releases(ctx context.Context, args ...string) []string {
	out, _ := p.git(ctx, append([]string{"tag"}, args...)...) //nolint:errcheck // no tag listed is no release, which the report says
	var names []string
	for _, t := range strings.Fields(string(out)) {
		if release.Is(t) {
			names = append(names, t)
		}
	}
	slices.Sort(names)
	return names
}

// pinned is the entry of a consumer's versions.yaml that names this repository.
type pinned struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
	Pin     string `yaml:"pin"`
	Track   string `yaml:"track"`
}

// report is what one consumer is on, a line a fact.
func report(ctx context.Context, t consumers.Table, c consumers.Consumer, s source, p producer) []string {
	as, err := s.name(ctx)
	if err != nil {
		return []string{"unread   " + err.Error()}
	}
	var lines []string
	if as != c.Repo {
		lines = append(lines, "read as  "+as)
	}
	pin, why, err := readPin(ctx, c, s, p.name)
	if err != nil {
		return append(lines, "unread   "+err.Error())
	}
	at := p.main
	switch {
	case why != "":
		lines = append(lines, fmt.Sprintf("pin      none: %s. Held to nothing, and compared with %s", why, p.main))
	default:
		if _, err := p.git(ctx, "cat-file", "-e", pin.Pin+"^{commit}"); err != nil {
			return append(lines, fmt.Sprintf("pin      %s (%s), which is no commit of %s in this clone", pin.Pin, pin.Version, p.name))
		}
		at = pin.Pin
		lines = append(lines, "pin      "+p.where(ctx, pin))
	}
	lines = append(lines, copies(ctx, c, s, p, at)...)
	if c.Packages != "" {
		pkgs, err := packages(ctx, t.Scopes, c, s, p, at)
		if err != nil {
			return append(lines, "unread   "+err.Error())
		}
		lines = append(lines, pkgs...)
	}
	return lines
}

// readPin is the consumer's entry for the repository it calls name, or why it has none.
func readPin(ctx context.Context, c consumers.Consumer, s source, name string) (pinned, string, error) {
	raw, found, err := s.read(ctx, c.Pin)
	if err != nil {
		return pinned{}, "", err
	}
	if !found {
		return pinned{}, "it has no " + c.Pin, nil
	}
	var entries []pinned
	if err := yaml.Unmarshal(raw, &entries); err != nil {
		return pinned{}, "", fmt.Errorf("%s: %w", c.Pin, err)
	}
	for _, e := range entries {
		if e.Name == name && e.Pin != "" {
			return e, "", nil
		}
	}
	return pinned{}, "its " + c.Pin + " has no " + name + " entry", nil
}

// where is a pin as a place in this repository's history: the release that names it, and how far
// main and the newest release have moved past it.
func (p producer) where(ctx context.Context, pin pinned) string {
	said := fmt.Sprintf("%s (%s, track %s)", short(pin.Pin), pin.Version, pin.Track)
	all := p.releases(ctx, "--merged", p.main)
	switch on := p.releases(ctx, "--points-at", pin.Pin); {
	case len(all) == 0:
		said += ", and " + p.name + " has no release"
	case len(on) == 0:
		said += ", which no release names; the newest is " + all[len(all)-1]
	case on[len(on)-1] == all[len(all)-1]:
		said += ", the newest release " + on[len(on)-1]
	default:
		said += fmt.Sprintf(", release %s; the newest is %s", on[len(on)-1], all[len(all)-1])
	}
	behind, err := p.git(ctx, "rev-list", "--count", pin.Pin+".."+p.main)
	if err != nil {
		return said + "; " + err.Error()
	}
	return fmt.Sprintf("%s; %s commits behind %s", said, strings.TrimSpace(string(behind)), p.main)
}

// copies holds each of the consumer's copies to what this repository had at commit at.
func copies(ctx context.Context, c consumers.Consumer, s source, p producer, at string) []string {
	if len(c.Copies) == 0 {
		return []string{"copies   none"}
	}
	var off []string
	for _, to := range slices.Sorted(maps.Keys(c.Copies)) {
		from := c.Copies[to]
		want, err := p.git(ctx, "show", at+":"+from)
		if err != nil {
			off = append(off, fmt.Sprintf("  %s: %s had no %s then", to, p.name, from))
			continue
		}
		switch got, found, err := s.read(ctx, to); {
		case err != nil:
			off = append(off, fmt.Sprintf("  %s: %v", to, err))
		case !found:
			off = append(off, fmt.Sprintf("  %s is not there (%s)", to, from))
		case !bytes.Equal(got, want):
			off = append(off, fmt.Sprintf("  %s is not %s", to, from))
		}
	}
	if len(off) == 0 {
		return []string{fmt.Sprintf("copies   all %d are %s's at %s", len(c.Copies), p.name, short(at))}
	}
	head := fmt.Sprintf("copies   %d of %d are not %s's at %s:", len(off), len(c.Copies), p.name, short(at))
	return append([]string{head}, off...)
}

// npm is where versions.yaml says an npm package comes from; the package's name is the path up
// to /-/.
const npm = "https://registry.npmjs.org/"

// packages holds what the consumer installs to the npm packages this repository's versions.yaml
// pinned at commit at - for brand, the faces the stylesheets of that commit name, which a
// consumer installs and does not copy. One it lacks or has at another version is said, and so is
// one of scopes it installs that was not pinned here.
func packages(ctx context.Context, scopes []string, c consumers.Consumer, s source, p producer, at string) ([]string, error) {
	want, err := p.pinnedPackages(ctx, at)
	if err != nil {
		return nil, err
	}
	raw, found, err := s.read(ctx, c.Packages)
	if err != nil {
		return nil, err
	}
	if !found {
		return []string{"packages it has no " + c.Packages}, nil
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return nil, fmt.Errorf("%s: %w", c.Packages, err)
	}
	has := map[string]string{}
	maps.Copy(has, pkg.DevDependencies)
	maps.Copy(has, pkg.Dependencies)

	var off []string
	for _, name := range slices.Sorted(maps.Keys(want)) {
		switch got, ok := has[name]; {
		case !ok:
			off = append(off, fmt.Sprintf("  %s %s is not installed", name, want[name]))
		case got != want[name]:
			off = append(off, fmt.Sprintf("  %s is %s, and %s's is %s", name, got, p.name, want[name]))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(has)) {
		scope, _, _ := strings.Cut(name, "/")
		if _, ok := want[name]; !ok && slices.Contains(scopes, scope) {
			off = append(off, fmt.Sprintf("  %s %s is no package %s pins", name, has[name], p.name))
		}
	}
	if len(off) == 0 {
		return []string{fmt.Sprintf("packages %s's at %s: %s", p.name, short(at), strings.Join(slices.Sorted(maps.Keys(want)), ", "))}, nil
	}
	head := fmt.Sprintf("packages not %s's at %s, which pins %d:", p.name, short(at), len(want))
	if len(want) == 0 {
		head = fmt.Sprintf("packages %s pinned none at %s, and it installs:", p.name, short(at))
	}
	return append([]string{head}, off...), nil
}

// pinnedPackages is the npm packages this repository's versions.yaml pinned at commit at, each
// with its version; none when it had no versions.yaml then.
func (p producer) pinnedPackages(ctx context.Context, at string) (map[string]string, error) {
	want := map[string]string{}
	raw, err := p.git(ctx, "show", at+":versions.yaml")
	if err != nil {
		return want, nil //nolint:nilerr // a commit before the repository pinned anything pinned no package
	}
	var entries []struct {
		Source  string `yaml:"source"`
		Version string `yaml:"version"`
	}
	if err := yaml.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("%s's versions.yaml at %s: %w", p.name, short(at), err)
	}
	for _, e := range entries {
		if rest, ok := strings.CutPrefix(e.Source, npm); ok {
			pkg, _, _ := strings.Cut(rest, "/-/")
			want[pkg] = e.Version
		}
	}
	return want, nil
}

func short(commit string) string {
	if len(commit) == 40 {
		return commit[:12]
	}
	return commit
}

func output(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // git and gh, with the table's paths and the pins read: arguments, not a command line
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", name, args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func run(ctx context.Context, args []string, w io.Writer) error {
	fs := flag.NewFlagSet("consumers", flag.ContinueOnError)
	table := fs.String("table", consumers.File, "the consumers")
	dir := fs.String("dir", ".", "a clone of this repository that holds the commits the consumers pin")
	ref := fs.String("main", "origin/main", "the ref a consumer is behind")
	local := map[string]checkout{}
	fs.Func("checkout", "owner/name=dir[@ref]: read that consumer from a checkout, at origin/HEAD or ref, and not through gh", func(v string) error {
		repo, at, ok := strings.Cut(v, "=")
		if !ok {
			return errors.New("want owner/name=dir[@ref]")
		}
		d, ref, ok := strings.Cut(at, "@")
		if !ok {
			ref = "origin/HEAD"
		}
		local[repo] = checkout{d, ref}
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return err
	}
	t, err := consumers.Load(*table)
	if err != nil {
		return err
	}
	p := producer{t.Name, *dir, *ref}
	head, err := p.git(ctx, "rev-parse", "--verify", p.main+"^{commit}")
	if err != nil {
		return err
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%s %s is %s\n", p.name, p.main, short(strings.TrimSpace(string(head))))
	for _, c := range t.Consumers {
		var s source = github{c.Repo}
		if co, ok := local[c.Repo]; ok {
			s = co
		}
		fmt.Fprintf(&out, "\n%s  %s\n", c.Name, c.Repo)
		for _, line := range report(ctx, t, c, s, p) {
			out.WriteString("  " + line + "\n")
		}
	}
	_, err = io.WriteString(w, out.String())
	return err
}

func main() {
	ctx := interrupt.Context()
	err := run(ctx, os.Args[1:], os.Stdout) // mutate-exempt: the process's own arguments; run is what the test drives
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "consumers:", err) //nolint:errcheck // the exit status says it
		}
		os.Exit(1) // mutate-exempt: any status but 0 fails the task; run is what the tests drive
	}
}
