// Package refs decides whether the prose this repository loads into every session still
// points at things that exist: a repository path, and a `task <name>`.
//
// It was 141 lines of shell, and the shell is why it is here. That script parsed `task
// --list-all --json` with a sed expression; when the expression matched nothing it reported
// every one of the 34 task references in the CLAUDE.md files as broken, which is the shape of
// a parser that read nothing and not of a tree that lost its tasks. The guard against that -
// an empty task list is never a true answer - is ErrNoTasks below, and it is a test rather
// than a paragraph. Nothing ran shellcheck over the script either, and it ran without `set -e`.
//
// What it decides has not changed, and neither has the file it reads: hack/refs-allow.txt
// still spells an exception `<file><TAB><reference>  # why`, and the ratchet still turns both
// ways - a reference in neither place fails, and an allowlist line that matches nothing fails
// too, so an entry cannot outlive the sentence it excuses.
package refs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Finding is one reference that does not resolve, named the way the allowlist spells it.
type Finding struct {
	// File is the prose that makes the reference, relative to the repository root.
	File string
	// Ref is the reference as written: a path, or "task <name>".
	Ref string
}

// Key is the allowlist's spelling of a finding: `<file><TAB><reference>`. An allowlist names the
// references made on purpose to something that is not here - naming a thing to say it is gone, or
// naming something in another repository.
func (f Finding) Key() string { return f.File + "\t" + f.Ref }

// TaskLister answers what `task` names resolve in a directory. It returns no error and no
// names for a directory with no Taskfile: a component that has none is not a failure.
type TaskLister func(dir string) ([]string, error)

// ErrNoTasks is the refusal this package exists to make: the root Taskfile has tasks, so an
// empty list means the list was not read, and every `task <name>` would then be reported
// broken. Saying "34 references are broken" because a parser matched nothing is a lie a
// reader acts on.
var ErrNoTasks = errors.New("refs: the root Taskfile listed no tasks, so no `task <name>` could resolve - this is a fault in the check, not in the files it reads")

// Options is what a run needs. Everything the filesystem and `task` are asked is injected, so
// the test drives a tree it wrote rather than this repository.
type Options struct {
	// Root is the repository root; every path in Files and every reference resolves under it.
	Root string
	// Files are the prose files to scan, relative to Root.
	Files []string
	// TopDirs are the repository's top-level directories. A reference is a path only if it
	// starts with one of these, so a module path like github.com/bufbuild/buf/cmd/buf is not
	// read as a `cmd/buf` this repository is missing.
	TopDirs []string
	// Tasks lists the task names of a directory, relative to Root.
	Tasks TaskLister
}

// extensions closes the last segment of a path reference. A segment carries no dot otherwise,
// so `internal/lifecycle.HostState` yields the package and not a file nobody named.
//
// dev and e2e are not file types; they are which lane a file belongs to, as in
// deploy/Caddyfile.e2e. Without them that name is read as deploy/Caddyfile, which nothing is,
// and the gate reports a file that exists as missing - a false finding, which costs a gate
// more than a missed one. The rest are what the repositories beside spin name: a kernel's
// host.config, a lock's apko.lock.json (one extension after another), a dictionary, a unit.
const extensions = `go|sh|sql|md|txt|yml|yaml|json|proto|dot|svg|js|ts|tsx|css|rules|dev|e2e|` +
	`config|conf|dict|lock|toml|env|d2|py|rs|service|mount|socket|timer|patch|hcl|tf|html`

// Check returns every reference in Files that resolves to nothing, sorted.
func Check(o Options) ([]Finding, error) {
	if len(o.TopDirs) == 0 {
		return nil, errors.New("refs: no top-level directories, so no reference could be recognised as a path")
	}
	rootTasks, err := o.Tasks(".")
	if err != nil {
		return nil, err
	}
	if len(rootTasks) == 0 {
		return nil, ErrNoTasks
	}

	pathRE, err := pathPattern(o.TopDirs)
	if err != nil {
		return nil, err
	}

	var found []Finding
	for _, file := range o.Files {
		body, err := os.ReadFile(filepath.Join(o.Root, file))
		if err != nil {
			return nil, fmt.Errorf("refs: reading %s: %w", file, err)
		}
		text := string(body)

		for _, ref := range pathRE.matches(text) {
			if resolves(o.Root, ref) {
				continue
			}
			found = append(found, Finding{File: file, Ref: ref})
		}

		// A component's file legitimately says "generate is a root task", so its own
		// Taskfile and the root's both answer.
		names, err := taskNames(o, rootTasks, path.Dir(file))
		if err != nil {
			return nil, err
		}
		for _, name := range taskRE.matches(text) {
			if names[name] {
				continue
			}
			found = append(found, Finding{File: file, Ref: "task " + name})
		}
	}

	sort.Slice(found, func(i, j int) bool { return found[i].Key() < found[j].Key() })
	return dedupe(found), nil
}

func taskNames(o Options, rootTasks []string, dir string) (map[string]bool, error) {
	names := make(map[string]bool, len(rootTasks))
	for _, n := range rootTasks {
		names[n] = true
	}
	if dir == "." {
		return names, nil
	}
	own, err := o.Tasks(dir)
	if err != nil {
		return nil, err
	}
	for _, n := range own {
		names[n] = true
	}
	return names, nil
}

// matcher pulls the interesting submatch out of a pattern whose first group is the character
// before the reference - RE2 has no lookbehind, so the character is matched and then dropped.
type matcher struct {
	re    *regexp.Regexp
	group int
}

func (m matcher) matches(text string) []string {
	var out []string
	for _, sub := range m.re.FindAllStringSubmatch(text, -1) { // mutate-exempt: any n < 0 is every match
		out = append(out, sub[m.group])
	}
	return out
}

func pathPattern(tops []string) (matcher, error) {
	quoted := make([]string, len(tops))
	for i, t := range tops {
		quoted[i] = regexp.QuoteMeta(t)
	}
	// A top-level directory followed by at least one segment, not preceded by a path
	// character. A segment may carry `*`: a glob names a set, and resolves when the set is
	// not empty.
	re, err := regexp.Compile(`(^|[^A-Za-z0-9_/.-])((?:` + strings.Join(quoted, "|") +
		`)(?:/[A-Za-z0-9_*-]+)+(?:\.(?:` + extensions + `))*)`)
	if err != nil {
		return matcher{}, fmt.Errorf("refs: building the path pattern: %w", err)
	}
	return matcher{re: re, group: 2}, nil
}

// taskRE reads a task name the way these files write a command: inside backticks. A flag is
// not a name, so `task --list` yields nothing.
var taskRE = matcher{re: regexp.MustCompile("`task ([a-z][A-Za-z0-9:_-]*)"), group: 1}

// resolves is whether the reference names something in the tree. A path git ignores is not in
// it even when it is on disk: it is what a build or a run left behind - the dashboard's copy in
// cmd/controlplane/web, for one - and reading it as present made the answer depend on whether
// whoever ran this had built first, so CI passed or failed on the order of its steps.
func resolves(root, ref string) bool {
	if !strings.Contains(ref, "*") {
		if _, err := os.Stat(filepath.Join(root, ref)); err != nil {
			return false
		}
		return !ignored(root, ref)
	}
	return globResolves(root, ref)
}

// ignored is whether git keeps this path out of the tree. Outside a repository - a test's
// temporary directory - nothing is ignored, and check-ignore says so by failing.
func ignored(root, ref string) bool {
	cmd := exec.Command("git", "check-ignore", "--quiet", "--", ref)
	cmd.Dir = root
	return cmd.Run() == nil
}

// globResolves walks from the last directory the pattern names literally, so `docs/**/*`
// reads docs/ and not the repository.
func globResolves(root, pattern string) bool {
	re, err := globRegexp(pattern)
	if err != nil {
		return false
	}
	start := literalPrefix(pattern)
	prefix := filepath.ToSlash(root) + "/"
	found := false
	// The walk's own errors are swallowed on purpose and only those: a subtree this process
	// cannot read is a reference that did not resolve, which is what the caller asked.
	_ = filepath.WalkDir(filepath.Join(root, start), func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return fs.SkipDir
		}
		rel := strings.TrimPrefix(filepath.ToSlash(p), prefix)
		if re.MatchString(rel) {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// literalPrefix is the directory part of the pattern before its first wildcard.
func literalPrefix(pattern string) string {
	i := strings.IndexByte(pattern, '*')
	if i < 0 { // mutate-exempt: a pattern starts with a top-level directory, so a * is never at 0
		return pattern
	}
	if j := strings.LastIndexByte(pattern[:i], '/'); j >= 0 {
		return pattern[:j]
	}
	return "."
}

// globRegexp gives `**` the meaning the shell's globstar gave it - any number of segments -
// and `*` the meaning it has everywhere else: anything within one segment.
func globRegexp(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString(`^`)
	for i := 0; i < len(pattern); i++ {
		switch {
		case strings.HasPrefix(pattern[i:], "**"):
			b.WriteString(`.*`)
			i++
		case pattern[i] == '*':
			b.WriteString(`[^/]*`)
		default:
			b.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		}
	}
	b.WriteString(`$`)
	return regexp.Compile(b.String())
}

func dedupe(in []Finding) []Finding {
	var out []Finding
	for i, f := range in {
		if i > 0 && in[i-1].Key() == f.Key() {
			continue
		}
		out = append(out, f)
	}
	return out
}
