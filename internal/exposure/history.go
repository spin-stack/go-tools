package exposure

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
)

// Finding is one private value, wherever it was found: Count places, the first of them First,
// and OnDefault when one of them is on the default branch - one that is not is in a pull
// request's head or another ref only, public all the same.
type Finding struct {
	Kind      string
	Value     string
	Count     int
	First     string // where it was first seen: a commit and file, or a page on GitHub
	OnDefault bool
}

// Key is how an allowlist names it: `<kind> <value>`, or `<kind> *` for every value of a kind.
// A value and not a line: history cannot be rewritten, so a value it holds is answered once,
// wherever it recurs.
func (f Finding) Key() string { return f.Kind + " " + f.Value }

func (f Finding) String() string {
	where := "on the default branch"
	if !f.OnDefault {
		where = "only off the default branch (a pull request's head, another ref): public all the same"
	}
	return fmt.Sprintf("%s %s: %d place(s), first %s, %s", f.Kind, f.Value, f.Count, f.First, where)
}

// tally gathers matches into findings by key, in the order each was first seen.
type tally struct {
	order []string
	by    map[string]*Finding
}

func (t *tally) add(kind, value, where string, onDefault bool) {
	if t.by == nil {
		t.by = map[string]*Finding{}
	}
	k := kind + " " + value
	f, ok := t.by[k]
	if !ok {
		f = &Finding{Kind: kind, Value: value, First: where}
		t.by[k] = f
		t.order = append(t.order, k)
	}
	f.Count++
	f.OnDefault = f.OnDefault || onDefault
}

func (t *tally) findings() []Finding {
	out := make([]Finding, 0, len(t.order))
	for _, k := range t.order {
		out = append(out, *t.by[k])
	}
	return out
}

// History is what a repository's commits show: its findings, and what only a person can judge.
type History struct {
	Commits, OffDefault int // commits any ref reaches, and those the default branch does not
	Findings            []Finding
	Deleted             []string // files some commit had and the default branch no longer has
	Binaries            []string // binary files some commit added: an image is read by looking
}

// ReadHistory reads every commit every ref of the repository at dir reaches - a mirror clone's
// refs/pull/* included - for the patterns, in every line a commit adds, and for its authors'
// and committers' addresses.
func ReadHistory(dir string, patterns []Pattern) (History, error) {
	var h History
	all, err := lines(dir, "rev-list", "--all")
	if err != nil {
		return h, err
	}
	onDefault, err := DefaultBranch(dir)
	if err != nil {
		return h, err
	}
	h.Commits = len(all)
	h.OffDefault = len(all) - len(onDefault)

	var t tally
	people, err := lines(dir, "log", "--all", "--format=%H %ae%n%H %ce")
	if err != nil {
		return h, err
	}
	for _, l := range people {
		commit, addr, _ := strings.Cut(l, " ")
		if !anonymous(addr) {
			t.add("author-email", addr, "commit "+short(commit), onDefault[commit])
		}
	}

	ever := map[string]bool{}
	binaries := map[string]bool{}
	err = patches(dir, func(commit, file string, kind added, line string) {
		switch kind {
		case touched:
			ever[file] = true
		case binary:
			binaries[file] = true
		default:
			for _, m := range Scan(patterns, line) {
				t.add(m.Kind, m.Value, fmt.Sprintf("commit %s %s", short(commit), file), onDefault[commit])
			}
		}
	})
	if err != nil {
		return h, err
	}
	tree, err := lines(dir, "ls-tree", "-r", "--name-only", "HEAD")
	if err != nil {
		return h, err
	}
	now := map[string]bool{}
	for _, f := range tree {
		now[f] = true
	}
	for f := range ever {
		if !now[f] {
			h.Deleted = append(h.Deleted, f)
		}
	}
	for f := range binaries {
		h.Binaries = append(h.Binaries, f)
	}
	sort.Strings(h.Deleted)
	sort.Strings(h.Binaries)
	h.Findings = t.findings()
	return h, nil
}

// DefaultBranch is the commits the default branch (HEAD) reaches.
func DefaultBranch(dir string) (map[string]bool, error) {
	head, err := lines(dir, "rev-list", "HEAD")
	if err != nil {
		return nil, err
	}
	on := map[string]bool{}
	for _, c := range head {
		on[c] = true
	}
	return on, nil
}

// added is what patches hands on: a file a commit touches, a binary file it adds, or a line of
// text it adds. Said apart, and not by the line's words: an added line can read like git's own.
type added int

const (
	touched added = iota
	binary
	text
)

// patches calls each with every file every commit touches and every line it adds, by commit and
// file. Read as a stream: a repository's whole history does not fit in memory as one string.
func patches(dir string, each func(commit, file string, kind added, line string)) error {
	cmd := exec.Command("git", "-C", dir, "log", "--all", "-p", "--no-color", "--no-ext-diff", "--format=commit %H")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr // mutate-exempt: what a failure says, beside the error that fails it
	if err := cmd.Start(); err != nil {
		return err
	}
	r := bufio.NewReaderSize(out, 1<<20) // mutate-exempt: a buffer's size, not what is read
	commit, file := "", ""
	for {
		line, err := r.ReadString('\n')
		line = strings.TrimSuffix(line, "\n")
		switch {
		case strings.HasPrefix(line, "commit "):
			commit, file = strings.TrimPrefix(line, "commit "), ""
		case strings.HasPrefix(line, "+++ b/"):
			file = strings.TrimPrefix(line, "+++ b/")
			each(commit, file, touched, "")
		case strings.HasPrefix(line, "Binary files "):
			// "Binary files a/x and b/x differ", or /dev/null on either side; one deleted has
			// no b/, and is not added.
			if _, name, ok := strings.Cut(line, " and b/"); ok {
				name = strings.TrimSuffix(name, " differ")
				each(commit, name, touched, "")
				each(commit, name, binary, "")
			}
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") && file != "":
			each(commit, file, text, line[1:])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git log: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func lines(dir string, args ...string) ([]string, error) {
	raw, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out, nil
}

func short(commit string) string { return commit[:min(len(commit), 9)] }
