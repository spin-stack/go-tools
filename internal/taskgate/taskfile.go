package taskgate

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/spin-stack/go-tools/internal/shell"
)

// Block is shell written inside a Taskfile that is longer than a task's few lines.
type Block struct {
	File  string
	Task  string // the task it is in, or vars for the file's own
	Line  int
	Lines int
	Limit int
}

// Key is <file>:<task>.
func (b Block) Key() string { return b.File + ":" + b.Task }

func (b Block) String() string {
	return fmt.Sprintf("%s:%d: task %s holds %d lines of shell (limit %d): a task says which commands run, in what order; a program goes in a script it calls, or in Go when it decides anything",
		b.File, b.Line, b.Task, b.Lines, b.Limit)
}

// Script is a script a task runs that is a program: longer than the limit, or one that decides -
// reads JSON, or writes what a workflow reads next - and so has an answer a test could hold.
type Script struct {
	File  string // the script, relative to the root
	By    string // <file>:<task> of the first task found running it
	Lines int
	Limit int
	Reads []string // the markers of a decision it has, in the order of the file
}

// Key is the script's path.
func (s Script) Key() string { return s.File }

func (s Script) String() string {
	var why []string
	if s.Limit > 0 && s.Lines > s.Limit {
		why = append(why, fmt.Sprintf("%d lines of shell (limit %d)", s.Lines, s.Limit))
	}
	if len(s.Reads) > 0 {
		why = append(why, "decides by "+strings.Join(s.Reads, ", "))
	}
	return fmt.Sprintf("%s:1: %s: a program, whose only test is the run that calls it (%s); Go, with a test",
		s.File, strings.Join(why, " and "), s.By)
}

// decides is what a shell says when it decides something: an answer handed to the workflow, or
// JSON read into the shell and judged there. Each is a function with a table test waiting.
var decides = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.])(GITHUB_OUTPUT|GITHUB_ENV|jq)(?:[^A-Za-z0-9_.]|$)`)

// taskfile is a file Task reads by name: the root's, a directory's, or one included by one.
func taskfile(rel string) bool {
	switch path.Base(rel) {
	case "Taskfile.yml", "Taskfile.yaml", "Taskfile.dist.yml", "Taskfile.dist.yaml":
		return true
	}
	return false
}

// tasks is the findings of the Taskfiles and of the scripts their tasks run.
func tasks(root string, files []string, listed map[string]bool, limits Limits) ([]Finding, error) {
	findings, scripts, err := blocks(root, files, listed, limits)
	if err != nil {
		return nil, err
	}
	for _, s := range scripts {
		raw, err := read(root, s.File)
		if err != nil {
			return nil, err
		}
		lines := shell.Lines(string(raw), 1) // mutate-exempt: a script's lines are counted, not placed
		s.Lines = len(lines)
		for _, l := range lines {
			if m := decides.FindStringSubmatch(l.Text); m != nil {
				s.Reads = append(s.Reads, m[1])
			}
		}
		s.Reads = unique(s.Reads)
		if (limits.Script > 0 && s.Lines > limits.Script) || len(s.Reads) > 0 {
			findings = append(findings, *s)
		}
	}
	return findings, nil
}

// blocks is the findings in the Taskfiles themselves - files and what they include, each read
// once - and the scripts their shell runs, each once, in the order first found.
func blocks(root string, files []string, listed map[string]bool, limits Limits) ([]Finding, []*Script, error) {
	seen := map[string]bool{}
	var queue []string
	add := func(rel string) {
		if listed[rel] && !seen[rel] {
			seen[rel] = true
			queue = append(queue, rel)
		}
	}
	for _, rel := range files {
		add(rel)
	}
	var findings []Finding
	var scripts []*Script
	known := map[string]bool{}
	for len(queue) > 0 {
		rel := queue[0]
		queue = queue[1:]
		raw, err := read(root, rel)
		if err != nil {
			return nil, nil, err
		}
		top, err := document(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("taskgate: %s: %w", rel, err)
		}
		if top == nil {
			continue
		}
		for _, inc := range includes(rel, top, listed) {
			add(inc)
		}
		for _, sh := range shells(top) {
			lines := shell.Lines(sh.node.Value, firstLine(sh.node))
			if limits.Block > 0 && len(lines) > limits.Block {
				findings = append(findings, Block{File: rel, Task: sh.task, Line: firstLine(sh.node), Lines: len(lines), Limit: limits.Block})
			}
			for _, l := range lines {
				for _, s := range called(rel, l.Text, listed) {
					if !known[s] {
						known[s] = true
						scripts = append(scripts, &Script{File: s, By: rel + ":" + sh.task, Limit: limits.Script})
					}
				}
			}
		}
	}
	return findings, scripts, nil
}

// includes is the Taskfiles rel includes, each relative to the root: `includes:` names a file git
// lists, or a directory whose Taskfile.yml it is, as a string or under `taskfile:`.
func includes(rel string, top *yaml.Node, listed map[string]bool) []string {
	inc := field(top, "includes")
	if inc == nil || inc.Kind != yaml.MappingNode {
		return nil
	}
	var out []string
	for i := 0; i+1 < len(inc.Content); i += 2 {
		v := inc.Content[i+1]
		if v.Kind == yaml.MappingNode {
			v = field(v, "taskfile")
		}
		if v == nil || v.Kind != yaml.ScalarNode || v.Value == "" {
			continue
		}
		p := path.Join(path.Dir(rel), v.Value)
		if !listed[p] {
			p = path.Join(p, "Taskfile.yml")
		}
		out = append(out, p)
	}
	return out
}

// shellIn is one scalar of shell in a Taskfile, and the task it is in.
type shellIn struct {
	task string
	node *yaml.Node
}

// shells is every scalar of shell in a Taskfile: each cmd of a task's cmds, as a string or under
// `cmd:`, and every `sh:` - a dynamic variable's, a precondition's - in a task or in the file's
// own vars.
func shells(top *yaml.Node) []shellIn {
	var out []shellIn
	if vars := field(top, "vars"); vars != nil {
		out = append(out, collect("vars", vars)...)
	}
	tasks := field(top, "tasks")
	if tasks == nil || tasks.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(tasks.Content); i += 2 {
		out = append(out, collect(tasks.Content[i].Value, tasks.Content[i+1])...)
	}
	return out
}

// collect is the shell under node, each as task's.
func collect(task string, node *yaml.Node) []shellIn {
	var out []shellIn
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, v := node.Content[i].Value, node.Content[i+1]
			switch {
			case key == "cmds" && v.Kind == yaml.SequenceNode:
				for _, item := range v.Content {
					if item.Kind == yaml.ScalarNode {
						out = append(out, shellIn{task, item})
					} else {
						out = append(out, collect(task, item)...)
					}
				}
			case (key == "sh" || key == "cmd") && v.Kind == yaml.ScalarNode:
				out = append(out, shellIn{task, v})
			case v.Kind != yaml.ScalarNode:
				out = append(out, collect(task, v)...)
			}
		}
	case yaml.SequenceNode:
		for _, item := range node.Content {
			out = append(out, collect(task, item)...)
		}
	}
	return out
}

// called is the scripts a line of shell in the Taskfile at rel runs: each word that ends in .sh
// and names a file git lists, relative to the Taskfile's directory - where Task runs it - or to
// the root.
func called(rel, line string, listed map[string]bool) []string {
	var out []string
	for _, w := range strings.Fields(line) {
		w = strings.Trim(w, `"'()$;&|`)
		if !strings.HasSuffix(w, ".sh") {
			continue
		}
		for _, p := range []string{path.Join(path.Dir(rel), w), path.Clean(w)} {
			if listed[p] {
				out = append(out, filepath.ToSlash(p))
				break
			}
		}
	}
	return out
}

func unique(words []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range words {
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	sort.Strings(out)
	return out
}
