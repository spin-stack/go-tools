// Package taskgate holds a repository's workflows to running its tasks: every run step of a
// workflow or a composite action is one `task <name>`, and what it does is written once, in the
// Taskfile, where a developer runs the same thing. A step that runs its own shell is a second
// copy of a build that goes out of step with the first, and is only ever run on a push.
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

// Finding is one run step that is not a task.
type Finding struct {
	File string
	Job  string // the job's id; empty in a composite action, which has none
	Step string // the step's name, its id, or #n, its place in the job from 1
	Line int
	Run  string
}

// Key is how an allowlist names the step: <file>/<job>/<step>, the job left out of a composite
// action's. Its line is not in it, so it survives an edit above it.
func (f Finding) Key() string {
	if f.Job == "" {
		return f.File + "/" + f.Step
	}
	return f.File + "/" + f.Job + "/" + f.Step
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: step %q runs %q: a step is one `task <name>`, and what it does is that task in the Taskfile, where a developer runs it too",
		f.File, f.Line, f.Step, f.Run)
}

// Check is every run step that is not one task, in the workflows and composite actions git lists
// under root, tracked or untracked and not ignored.
func Check(root string) ([]Finding, error) {
	out, err := exec.Command("git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", ".github").Output()
	if err != nil {
		return nil, fmt.Errorf("taskgate: listing %s: %w", root, err)
	}
	var findings []Finding
	for _, rel := range strings.Fields(string(out)) {
		if !workflow(rel) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			if os.IsNotExist(err) {
				continue // deleted, not yet staged
			}
			return nil, fmt.Errorf("taskgate: %w", err)
		}
		found, err := steps(rel, raw)
		if err != nil {
			return nil, fmt.Errorf("taskgate: %s: %w", rel, err)
		}
		findings = append(findings, found...)
	}
	return findings, nil
}

func workflow(rel string) bool {
	yml := path.Ext(rel) == ".yml" || path.Ext(rel) == ".yaml"
	name := strings.TrimSuffix(path.Base(rel), path.Ext(rel))
	return yml && (path.Dir(rel) == ".github/workflows" || strings.HasPrefix(rel, ".github/actions/") && name == "action")
}

// steps is the findings of one file: a workflow's jobs' steps, or a composite action's.
func steps(rel string, raw []byte) ([]Finding, error) {
	var doc yaml.Node
	if err := yaml.NewDecoder(bytes.NewReader(raw)).Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil // an empty file runs nothing
		}
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	top := doc.Content[0]
	var out []Finding
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

// judge is the findings of one job's steps.
func judge(rel, job string, steps *yaml.Node) []Finding {
	if steps == nil || steps.Kind != yaml.SequenceNode {
		return nil
	}
	var out []Finding
	for i, step := range steps.Content {
		run := field(step, "run")
		if run == nil {
			continue
		}
		first := run.Line
		if run.Style == yaml.LiteralStyle || run.Style == yaml.FoldedStyle {
			first++ // the text starts under the | or the >
		}
		lines := shell.Lines(run.Value, first)
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
		out = append(out, Finding{File: rel, Job: job, Step: name, Line: first, Run: said})
	}
	return out
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
