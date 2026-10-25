// Package taskgate holds a repository's workflows to running its tasks, and its tasks to being
// commands rather than programs. Every run step of a workflow or a composite action is one
// `task <name>`, and what it does is written once, in the Taskfile, where a developer runs the
// same thing: a step that runs its own shell is a second copy of a build that goes out of step
// with the first, and is only ever run on a push. A task's shell is a few lines that say which
// commands run and in what order; a program written inside a Taskfile, or in a script a task
// calls, has no test - and one that decides from what it reads is the program whose only test
// was the day it ran on main.
package taskgate

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/spin-stack/go-tools/internal/shell"
)

// Finding is one thing the gate holds against: a Step that is not a task, a Block of shell in a
// Taskfile over the limit, or a Script a task runs that is a program. Key names it as an allowlist
// does, without a line, so an entry survives an edit above it.
type Finding interface {
	Key() string
	String() string
}

// Step is one run step that is not a task.
type Step struct {
	File string
	Job  string // the job's id; empty in a composite action, which has none
	Step string // the step's name, its id, or #n, its place in the job from 1
	Line int
	Run  string
}

// Key is <file>/<job>/<step>, the job left out of a composite action's.
func (s Step) Key() string {
	if s.Job == "" {
		return s.File + "/" + s.Step
	}
	return s.File + "/" + s.Job + "/" + s.Step
}

func (s Step) String() string {
	return fmt.Sprintf("%s:%d: step %q runs %q: a step is one `task <name>`, and what it does is that task in the Taskfile, where a developer runs it too",
		s.File, s.Line, s.Step, s.Run)
}

// Limits is how much shell a task may hold before it is a program: Block lines in a Taskfile's
// own cmd or sh, Script lines in a script a task runs. Lines a shell runs, as shell.Lines counts
// them: comments and blank lines are not among them. Zero is no limit.
type Limits struct {
	Block  int
	Script int
}

// Check is every finding under root: in the workflows and composite actions, the Taskfiles and
// the scripts their tasks run - what git lists, tracked or untracked and not ignored.
func Check(root string, limits Limits) ([]Finding, error) {
	out, err := exec.Command("git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		return nil, fmt.Errorf("taskgate: listing %s: %w", root, err)
	}
	listed := map[string]bool{}
	var workflows, taskfiles []string
	for _, rel := range strings.Fields(string(out)) {
		listed[rel] = true
		switch {
		case workflow(rel):
			workflows = append(workflows, rel)
		case taskfile(rel):
			taskfiles = append(taskfiles, rel)
		}
	}
	var findings []Finding
	for _, rel := range workflows {
		raw, err := read(root, rel)
		if err != nil {
			return nil, err
		}
		found, err := steps(rel, raw)
		if err != nil {
			return nil, fmt.Errorf("taskgate: %s: %w", rel, err)
		}
		for _, f := range found {
			findings = append(findings, f)
		}
	}
	found, err := tasks(root, taskfiles, listed, limits)
	if err != nil {
		return nil, err
	}
	return append(findings, found...), nil
}

// read is rel's bytes, or none for a file git lists that is gone - deleted, not yet staged - or
// that is a directory - a submodule, which git lists by its path - neither of which holds shell.
func read(root, rel string) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		if fi, serr := os.Stat(filepath.Join(root, rel)); os.IsNotExist(err) || serr == nil && fi.IsDir() {
			return nil, nil
		}
		return nil, fmt.Errorf("taskgate: %w", err)
	}
	return raw, nil
}

func workflow(rel string) bool {
	yml := path.Ext(rel) == ".yml" || path.Ext(rel) == ".yaml"
	name := strings.TrimSuffix(path.Base(rel), path.Ext(rel))
	return yml && (path.Dir(rel) == ".github/workflows" || strings.HasPrefix(rel, ".github/actions/") && name == "action")
}

// steps is the findings of one file: a workflow's jobs' steps, or a composite action's.
func steps(rel string, raw []byte) ([]Step, error) {
	top, err := document(raw)
	if err != nil || top == nil {
		return nil, err
	}
	var out []Step
	if runs := field(top, "runs"); runs != nil {
		out = append(out, judge(rel, "", field(runs, "steps"))...)
	}
	if jobs := field(top, "jobs"); jobs != nil && jobs.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(jobs.Content); i += 2 {
			out = append(out, judge(rel, jobs.Content[i].Value, field(jobs.Content[i+1], "steps"))...)
		}
	}
	return out, nil
}

// document is the top node of a YAML file, or nil for an empty one, which holds nothing.
func document(raw []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.NewDecoder(bytes.NewReader(raw)).Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	return doc.Content[0], nil
}

// judge is the findings of one job's steps.
func judge(rel, job string, steps *yaml.Node) []Step {
	if steps == nil || steps.Kind != yaml.SequenceNode {
		return nil
	}
	var out []Step
	for i, step := range steps.Content {
		run := field(step, "run")
		if run == nil {
			continue
		}
		lines := shell.Lines(run.Value, firstLine(run))
		if len(lines) == 1 && isTask(lines[0].Text) {
			continue
		}
		name := "#" + strconv.Itoa(i+1)
		if n := field(step, "id"); n != nil {
			name = n.Value
		}
		if n := field(step, "name"); n != nil {
			name = n.Value
		}
		said := strings.TrimSpace(run.Value)
		if len(lines) > 0 {
			said = lines[0].Text
			if len(lines) > 1 {
				said += " ..."
			}
		}
		out = append(out, Step{File: rel, Job: job, Step: name, Line: firstLine(run), Run: said})
	}
	return out
}

// firstLine is the line a scalar's text starts on: under the | or the > of a block scalar.
func firstLine(n *yaml.Node) int {
	if n.Style == yaml.LiteralStyle || n.Style == yaml.FoldedStyle {
		return n.Line + 1
	}
	return n.Line
}

// isTask is a line that runs Task and nothing else: task, or the Task go.mod pins (go tool task),
// with what it is given.
func isTask(line string) bool {
	line = strings.TrimPrefix(line, "go tool ")
	if !strings.HasPrefix(line+" ", "task ") {
		return false
	}
	for _, w := range strings.Fields(line) {
		// One command: a second one after it is shell around the task, not the task.
		if w == "&&" || w == "||" || w == ";" || w == "|" || strings.HasPrefix(w, ">") {
			return false
		}
	}
	return true
}

func field(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}
