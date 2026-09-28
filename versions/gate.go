package versions

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Gate holds a repository to its versions.yaml: a digest or a commit written anywhere else is a
// pin a bump does not move and check does not see; a Dockerfile's pin with a default of its own
// is one a build can fall back to without anybody noticing; an entry nothing reads is a pin
// nobody checks; an entry written so a bump cannot rewrite it in place is one that cannot move. A repository runs it from a test, so `go test` is what fails.
type Gate struct {
	// Root is the repository's top, where versions.yaml is.
	Root string
	// Elsewhere are the files, relative to Root, that may write a digest or a commit because
	// another tool writes them (go.sum, a lock) or they record something that happened rather
	// than pin what is built. One that ends in / is a directory, and everything under it.
	Elsewhere []string
	// Reads are what says an entry is read: each pattern's first group is one or more names,
	// separated by spaces. Reads is DefaultReads when nil.
	Reads []*regexp.Regexp
}

// DefaultReads is how a repository reads versions.yaml through this module's command: `versions
// args go kernel` in a Taskfile, or `{{.VERSIONS}} args ...` where a variable runs it.
var DefaultReads = []*regexp.Regexp{
	regexp.MustCompile(`(?:versions|\{\{\.VERSIONS\}\}) (?:args|env|ref|version)((?: [a-z][a-z0-9-]*)+)`),
}

var (
	pinned = regexp.MustCompile(`\b[0-9a-f]{64}\b|\b[0-9a-f]{40}\b`)
	// A Dockerfile's build argument that is one of an entry's (Entry.Args).
	pinArg = regexp.MustCompile(`(?m)^ARG ([A-Z0-9_]+(?:_IMAGE|_VERSION|_COMMIT|_SHA256|_SNAPSHOT))(=.*)?$`)
	// An action a workflow runs, by its commit: Dependabot's to move, as go.sum is Go's.
	actionPin = regexp.MustCompile(`^\s*(?:-\s*)?uses:\s*[^@\s]+@[0-9a-f]{40}(?:\s|$)`)
)

// Check is every way the repository breaks the gate, joined; nil when it breaks none.
func (g Gate) Check() error {
	v, err := Load(filepath.Join(g.Root, File))
	if err != nil {
		return err
	}
	files, err := g.files()
	if err != nil {
		return err
	}
	reads := g.Reads
	if reads == nil {
		reads = DefaultReads
	}
	args := map[string]bool{}
	for _, e := range v.Entries {
		for k := range e.Args() {
			args[k] = true
		}
	}
	var errs []error
	// Each entry is one a bump can rewrite where it is written: one that cannot is found here
	// rather than on the day it is behind.
	for _, e := range v.Entries {
		if _, err := v.Set(e.Name, e.Version, e.Pin); err != nil {
			errs = append(errs, err)
		}
	}
	used := map[string]bool{}
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		// A test's digests are fixtures, but what it reads of versions.yaml is read.
		if !strings.HasSuffix(rel, "_test.go") {
			errs = append(errs, pins(rel, files[rel], args)...)
		}
		for _, re := range reads {
			for _, m := range re.FindAllStringSubmatch(files[rel], -1) {
				for _, n := range strings.Fields(m[1]) {
					if _, err := v.Get(n); err != nil && !used[n] {
						errs = append(errs, fmt.Errorf("%s reads %s: %w", rel, n, err))
					}
					used[n] = true
				}
			}
		}
	}
	for _, e := range v.Entries {
		if !used[e.Name] {
			errs = append(errs, fmt.Errorf("%s pins %s, and nothing reads it", File, e.Name))
		}
	}
	return errors.Join(errs...)
}

// pins is what the file rel breaks of the gate by what it writes: a digest or a commit, or a
// Dockerfile's pin that is not one of args, the build arguments versions.yaml hands in, or has a
// default.
func pins(rel, body string, args map[string]bool) []error {
	var errs []error
	for i, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(rel, ".github/workflows/") && actionPin.MatchString(line) {
			continue
		}
		if m := pinned.FindString(line); m != "" {
			errs = append(errs, fmt.Errorf("%s:%d pins %s: it belongs in %s", rel, i+1, m, File))
		}
	}
	if filepath.Base(rel) == "Dockerfile" || strings.HasSuffix(rel, ".Dockerfile") {
		for _, m := range pinArg.FindAllStringSubmatch(body, -1) {
			if m[2] != "" {
				errs = append(errs, fmt.Errorf("%s: ARG %s has a default; %s hands it in", rel, m[1], File))
			}
			if !args[m[1]] {
				errs = append(errs, fmt.Errorf("%s: ARG %s is no entry's: %s has nothing to hand it", rel, m[1], File))
			}
		}
	}
	return errs
}

// files is every file of the repository a pin could be written or read in. _output and the
// dot-directories but .github are what a build or a tool made.
func (g Gate) files() (map[string]string, error) {
	top, err := os.OpenRoot(g.Root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = top.Close() }() // read-only
	out := map[string]string{}
	err = fs.WalkDir(top.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if rel == "_output" || (strings.HasPrefix(d.Name(), ".") && rel != "." && rel != ".github") ||
				slices.Contains(g.Elsewhere, rel+"/") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || rel == File || slices.Contains(g.Elsewhere, rel) {
			return nil
		}
		raw, err := top.ReadFile(rel)
		if err != nil {
			return err
		}
		out[rel] = string(raw)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("versions: %w", err)
	}
	return out, nil
}
