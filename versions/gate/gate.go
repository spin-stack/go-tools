// Package gate holds a repository to its versions.yaml, from a test: a pin written anywhere else
// fails it. It is apart from versions so that a program which embeds its pins does not link it.
package gate

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/spin-stack/go-tools/internal/tasks"
	"github.com/spin-stack/go-tools/versions"
)

// Gate holds a repository to its versions.yaml: a digest or a commit written anywhere else is a
// pin a bump does not move and check does not see; a Dockerfile's pin with a default of its own
// is one a build can fall back to without anybody noticing; an entry nothing reads is a pin
// nobody checks; an entry written so a bump cannot rewrite it in place is one that cannot move;
// an entry tracked with another and not at its version is a build of two releases; an entry a
// common task of go-tools reads and the repository that includes those tasks lacks is a task that
// cannot run. A repository runs it from a test, so `go test` is what fails.
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
// args go kernel` in a Taskfile, or `{{.VERSIONS}} args ...` where a variable runs it. Two
// patterns and not one alternation, so that each starts with a literal the regexp package skips
// to: the gate reads every file of a repository, under the race detector in a test.
var DefaultReads = []*regexp.Regexp{
	regexp.MustCompile(`versions (?:args|env|ref|version)((?: [a-z][a-z0-9-]*)+)`),
	regexp.MustCompile(`\{\{\.VERSIONS\}\} (?:args|env|ref|version)((?: [a-z][a-z0-9-]*)+)`),
}

var (
	// A Dockerfile's build argument that is one of an entry's (Entry.Args).
	pinArg = regexp.MustCompile(`(?m)^ARG ([A-Z0-9_]+(?:_IMAGE|_VERSION|_COMMIT|_SHA256|_SNAPSHOT))(=.*)?$`)
	// An action a workflow or a composite action runs, by its commit: Dependabot's to move, as
	// go.sum is Go's.
	actionPin = regexp.MustCompile(`^\s*(?:-\s*)?uses:\s*[^@\s]+@[0-9a-f]{40}(?:\s|$)`)
)

// Check is every way the repository breaks the gate, joined; nil when it breaks none.
func (g Gate) Check() error {
	v, err := versions.Load(filepath.Join(g.Root, versions.File))
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
	// An entry tracked with another is at what that one is at: a bump moves both, and a hand that
	// moved one alone has written a build of two releases.
	errs = append(errs, apart(v)...)
	used := map[string]bool{}
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		// A test's digests are fixtures, but what it reads of versions.yaml is read.
		if !strings.HasSuffix(rel, "_test.go") {
			errs = append(errs, pins(rel, files[rel], args)...)
		}
		for _, re := range reads {
			// mutate-exempt: any n below zero is every match.
			for _, m := range re.FindAllStringSubmatch(files[rel], -1) {
				for n := range strings.FieldsSeq(m[1]) {
					if _, err := v.Get(n); err != nil && !used[n] {
						errs = append(errs, fmt.Errorf("%s reads %s: %w", rel, n, err))
					}
					used[n] = true
				}
			}
		}
	}
	taskCommand(files["go.mod"], used)
	errs = append(errs, commonTasks(v, files, tasks.File, used)...)
	for _, e := range v.Entries {
		if !used[e.Name] {
			errs = append(errs, fmt.Errorf("%s pins %s, and nothing reads it", versions.File, e.Name))
		}
	}
	return errors.Join(errs...)
}

// apart is each entry tracked with another in the file that is not at that one's version. One with
// another repository's entry is check's: the gate reaches nothing over the network.
func apart(v *versions.Versions) []error {
	var errs []error
	for _, e := range v.Entries {
		a, ok, _ := e.With()
		if !ok || a.Repo != "" {
			continue
		}
		if want, _ := v.Expected(e); e.Version != want {
			errs = append(errs, fmt.Errorf("%s: %s is with %s, so it is at %q, not %q: versions bump %s moves both",
				versions.File, e.Name, a.Name, want, e.Version, a.Name))
		}
	}
	return errs
}

// TaskCommand is the go-tools command a go.mod takes as `go tool task`: the Task go-tools pins, or
// versions.yaml's `task` when there is one, with go-tools' latest release on its PATH.
const TaskCommand = "github.com/spin-stack/go-tools/cmd/task"

// taskCommand marks versions.yaml's `task` used when go.mod takes go-tools' task command: the
// command reads that entry, which no line of the repository spells as a read. A repository owes
// none, since the command carries its own Task; go-tools itself is its latest release, which no
// versions.yaml pins.
func taskCommand(gomod string, used map[string]bool) {
	if slices.ContainsFunc(strings.Fields(gomod), func(f string) bool { return f == TaskCommand }) {
		used["task"] = true
	}
}

// tasksInclude is what a Taskfile includes to have go-tools' common tasks: the variable
// `go tool task` names the release's tasks.yml in.
const tasksInclude = "{{.GO_TOOLS_TASKS}}"

// commonTasks is what a repository whose Taskfile includes go-tools' common tasks owes
// versions.yaml: every entry one of those tasks reads. The reads are in common, the tasks' file,
// which is in a release and in no file of the repository, so each entry is marked used here; and a
// repository without one has a task that fails the day it is run, which is said here instead.
func commonTasks(v *versions.Versions, files map[string]string, common []byte, used map[string]bool) []error {
	by := includer(files)
	if by == "" {
		return nil
	}
	reads, err := taskReads(common)
	if err != nil {
		return []error{err}
	}
	var errs []error
	for _, r := range reads {
		used[r.entry] = true
		if _, err := v.Get(r.entry); err != nil {
			errs = append(errs, fmt.Errorf("%s includes go-tools' tasks, and its task %s reads %s: %w", by, r.task, r.entry, err))
		}
	}
	return errs
}

// includer is the file of the repository that includes go-tools' common tasks, "" when none
// does: a Taskfile with tasksInclude as the taskfile of one of its includes. A file that only
// mentions the variable - a comment, a page that shows the include - includes nothing.
func includer(files map[string]string) string {
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		// Only a file that spells the variable is parsed: the gate reads every file of a repository.
		if !strings.Contains(files[rel], "GO_TOOLS_TASKS") {
			continue
		}
		var taskfile struct {
			Includes map[string]yaml.Node `yaml:"includes"`
		}
		_ = yaml.Unmarshal([]byte(files[rel]), &taskfile) // what is not a Taskfile has no includes
		for _, include := range taskfile.Includes {
			// An include is its file, or a mapping whose taskfile is: one of the two decodes.
			var short string
			var long struct {
				Taskfile string `yaml:"taskfile"`
			}
			_, _ = include.Decode(&short), include.Decode(&long)
			if strings.Join(strings.Fields(short+long.Taskfile), "") == tasksInclude {
				return rel
			}
		}
	}
	return ""
}

// A taskRead is a task and an entry of versions.yaml it reads.
type taskRead struct{ task, entry string }

// taskReads is every read of versions.yaml in taskfile, a Taskfile's bytes, by the task that
// makes it and once each, as DefaultReads finds them in that task: a common task runs the versions
// command and no repository's own way of reading. In the order of the tasks' names.
func taskReads(taskfile []byte) ([]taskRead, error) {
	var file struct {
		Tasks map[string]any `yaml:"tasks"`
	}
	if err := yaml.Unmarshal(taskfile, &file); err != nil {
		return nil, fmt.Errorf("versions: go-tools' common tasks: %w", err)
	}
	var reads []taskRead
	for _, task := range slices.Sorted(maps.Keys(file.Tasks)) {
		text := fmt.Sprint(file.Tasks[task])
		for _, re := range DefaultReads {
			// mutate-exempt: any n below zero is every match.
			for _, m := range re.FindAllStringSubmatch(text, -1) {
				for entry := range strings.FieldsSeq(m[1]) {
					if r := (taskRead{task, entry}); !slices.Contains(reads, r) {
						reads = append(reads, r)
					}
				}
			}
		}
	}
	return reads, nil
}

// pins is what the file rel breaks of the gate by what it writes: a digest or a commit, or a
// Dockerfile's pin that is not one of args, the build arguments versions.yaml hands in, or has a
// default.
func pins(rel, body string, args map[string]bool) []error {
	var errs []error
	n := 0
	for line := range strings.Lines(body) {
		n++
		m := firstPin(line)
		if m == "" || (strings.HasPrefix(rel, ".github/") && actionPin.MatchString(line)) {
			continue
		}
		errs = append(errs, fmt.Errorf("%s:%d pins %s: it belongs in %s", rel, n, m, versions.File))
	}
	if filepath.Base(rel) == "Dockerfile" || strings.HasSuffix(rel, ".Dockerfile") {
		// mutate-exempt: any n below zero is every match.
		for _, m := range pinArg.FindAllStringSubmatch(body, -1) {
			if m[2] != "" {
				errs = append(errs, fmt.Errorf("%s: ARG %s has a default; %s hands it in", rel, m[1], versions.File))
			}
			if !args[m[1]] {
				errs = append(errs, fmt.Errorf("%s: ARG %s is no entry's: %s has nothing to hand it", rel, m[1], versions.File))
			}
		}
	}
	return errs
}

// firstPin is the first digest or commit in line: a word of 64 or of 40 lowercase hex digits,
// what `\b[0-9a-f]{64}\b|\b[0-9a-f]{40}\b` finds. Read without a regexp: the gate reads every line
// of a repository, and under the race detector that regexp made it slower than the tests it
// runs beside.
func firstPin(line string) string {
	for i := 0; i < len(line); {
		if !isWord(line[i]) {
			i++
			continue
		}
		j, hex := i, true
		for ; j < len(line) && isWord(line[j]); j++ {
			hex = hex && ('0' <= line[j] && line[j] <= '9' || 'a' <= line[j] && line[j] <= 'f')
		}
		if hex && (j-i == 64 || j-i == 40) {
			return line[i:j]
		}
		i = j
	}
	return ""
}

// isWord is whether c is a character of a word, as the regexp package's \b says.
func isWord(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '_'
}

// binary is whether body is not text: a NUL in its first 8 KiB, as git and grep decide it. An
// image's bytes are nobody's pin and nobody's read.
func binary(body string) bool {
	return strings.IndexByte(body[:min(len(body), 8<<10)], 0) >= 0
}

// files is every file of the repository a pin could be written or read in: what git tracks, and
// what it would if added, so that a new file is held to the gate before its commit and a
// dependency tree or a build's output that git ignores (node_modules, bin/) is not read at all.
// _output and the dot-directories but .github are what a build or a tool made even when git is
// not told so.
func (g Gate) files() (map[string]string, error) {
	cmd := exec.Command("git", "-C", g.Root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	list, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("versions: the gate reads the files git lists in %s: %w: %s", g.Root, err, strings.TrimSpace(stderr.String()))
	}
	top, err := os.OpenRoot(g.Root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = top.Close() }() // read-only
	out := map[string]string{}
	for rel := range strings.SplitSeq(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if rel == "" || rel == versions.File || g.exempt(rel) {
			continue
		}
		info, err := top.Lstat(rel)
		// A file deleted and not yet staged is still in git's index.
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("versions: %w", err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		raw, err := top.ReadFile(rel)
		if err != nil {
			return nil, fmt.Errorf("versions: %w", err)
		}
		if !binary(string(raw)) {
			out[rel] = string(raw)
		}
	}
	return out, nil
}

// exempt is whether rel is one of Elsewhere's, or in a directory that a build or a tool made.
func (g Gate) exempt(rel string) bool {
	for _, e := range g.Elsewhere {
		if rel == e || (strings.HasSuffix(e, "/") && strings.HasPrefix(rel, e)) {
			return true
		}
	}
	dirs := strings.Split(rel, "/")
	for i, d := range dirs[:len(dirs)-1] {
		if (i == 0 && d == "_output") || (strings.HasPrefix(d, ".") && (i != 0 || d != ".github")) {
			return true
		}
	}
	return false
}
