package sfw

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Finding is one command that breaks the rule, where it is written.
type Finding struct {
	File    string
	Line    int
	Command string
	// Workflow is a package manager run from a workflow, which is to be a task whatever it does;
	// otherwise the command fetches packages without the firewall.
	Workflow bool
}

func (f Finding) String() string {
	if f.Workflow {
		return fmt.Sprintf("%s:%d: %q runs a package manager from the workflow: make it a task, and run the task", f.File, f.Line, f.Command)
	}
	return fmt.Sprintf("%s:%d: %q fetches packages without Socket Firewall: run it as `go tool sfw %s`", f.File, f.Line, f.Command, f.Command)
}

// managers are the commands that fetch from a JavaScript registry, each with the subcommands
// that do: nil is every use of it. A subcommand that only runs what is installed - build, run,
// test, pnpm exec - reaches no registry and is left alone.
var managers = map[string][]string{
	"npm":  {"install", "i", "ci", "add", "update", "up", "upgrade", "exec", "x", "create", "init"},
	"pnpm": {"install", "i", "add", "update", "up", "upgrade", "dlx", "create", "fetch"},
	"yarn": {"", "install", "add", "up", "upgrade", "dlx", "create"},
	"npx":  nil,
	"pnpx": nil,
}

// valued are the flags before a subcommand that take the word after them: `pnpm -C dashboard
// install` installs.
var valued = []string{"-C", "--dir", "--prefix", "--cwd", "-F", "--filter", "--workspace"}

// Check is every finding in the files git lists under root - tracked, or untracked and not
// ignored - that run commands: workflows and composite actions, Taskfiles, shell scripts and
// Dockerfiles. What a workflow runs is to be a task, so a developer runs what CI runs; anything
// that fetches packages is to go through the firewall, wherever it is written.
func Check(root string) ([]Finding, error) {
	out, err := exec.Command("git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		return nil, fmt.Errorf("sfw: listing %s: %w", root, err)
	}
	var findings []Finding
	for _, rel := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		kind := kindOf(rel)
		if kind == none {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			if os.IsNotExist(err) {
				continue // deleted, not yet staged
			}
			return nil, fmt.Errorf("sfw: %w", err)
		}
		lines, err := commands(kind, raw)
		if err != nil {
			return nil, fmt.Errorf("sfw: %s: %w", rel, err)
		}
		for _, l := range lines {
			for _, cmd := range segments(l.text) {
				if f, ok := judge(cmd, kind == workflow); ok {
					findings = append(findings, Finding{File: rel, Line: l.line, Command: f, Workflow: kind == workflow})
				}
			}
		}
	}
	return findings, nil
}

type kind int

const (
	none kind = iota
	workflow
	taskfile
	script
)

func kindOf(rel string) kind {
	base := path.Base(rel)
	switch {
	case slices.Contains(strings.Split(rel, "/"), "testdata"):
		return none
	case strings.HasPrefix(rel, ".github/workflows/") && (strings.HasSuffix(rel, ".yml") || strings.HasSuffix(rel, ".yaml")),
		strings.HasPrefix(rel, ".github/actions/") && (base == "action.yml" || base == "action.yaml"):
		return workflow
	case strings.HasPrefix(base, "Taskfile") && (strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".yaml")):
		return taskfile
	case strings.HasSuffix(base, ".sh"), strings.HasPrefix(base, "Dockerfile"), strings.HasSuffix(base, ".Dockerfile"):
		return script
	}
	return none
}

// line is one line of shell, with where it is written.
type line struct {
	text string
	line int
}

// commands is the shell a file runs: a workflow's and a Taskfile's are the strings under run,
// cmd, sh and cmds, and not a description that names a command; a script's is the whole file.
func commands(k kind, raw []byte) ([]line, error) {
	if k == script {
		return shellLines(string(raw), 1), nil
	}
	var doc yaml.Node
	if err := yaml.NewDecoder(bytes.NewReader(raw)).Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil // an empty file runs nothing
		}
		return nil, err
	}
	var out []line
	var walk func(n *yaml.Node, key string)
	walk = func(n *yaml.Node, key string) {
		switch n.Kind {
		case yaml.DocumentNode:
			for _, c := range n.Content {
				walk(c, key)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				walk(n.Content[i+1], n.Content[i].Value)
			}
		case yaml.SequenceNode:
			for _, c := range n.Content {
				// A cmds item is a command, or a mapping whose cmd is.
				walk(c, key)
			}
		case yaml.ScalarNode:
			if key == "run" || key == "cmd" || key == "cmds" || key == "sh" {
				first := n.Line
				if n.Style == yaml.LiteralStyle || n.Style == yaml.FoldedStyle {
					first++ // the text starts under the | or the >
				}
				out = append(out, shellLines(n.Value, first)...)
			}
		case yaml.AliasNode:
		}
	}
	walk(&doc, "")
	return out, nil
}

// shellLines is text as the lines a shell runs: a line continued with a backslash joined to the
// next, and a comment dropped.
func shellLines(text string, first int) []line {
	var out []line
	var cur strings.Builder
	start := first
	for i, l := range strings.Split(text, "\n") {
		if cur.Len() == 0 {
			start = first + i
		}
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		if strings.HasSuffix(l, "\\") {
			cur.WriteString(strings.TrimSuffix(l, "\\") + " ")
			continue
		}
		cur.WriteString(l)
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, line{s, start})
		}
		cur.Reset()
	}
	return out
}

// segments is each simple command in a line: what is between &&, ||, ;, |, and inside $( ).
// A Dockerfile's RUN is the shell after it.
func segments(text string) []string {
	text = strings.NewReplacer("$(", "\n", "`", "\n", ")", "\n", "&&", "\n", "||", "\n", ";", "\n", "|", "\n").Replace(text)
	var out []string
	for _, s := range strings.Split(text, "\n") {
		s = strings.TrimSpace(s)
		s = strings.TrimSpace(strings.TrimPrefix(s, "RUN "))
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// judge says whether cmd breaks the rule, and the command as the finding names it: from a
// workflow, any package manager does, the firewall's included; elsewhere, one that fetches and is
// not run through the firewall.
func judge(cmd string, fromWorkflow bool) (string, bool) {
	words, wrapped := unwrap(strings.Fields(cmd))
	if len(words) == 0 {
		return "", false
	}
	subs, ok := managers[words[0]]
	if !ok {
		return "", false
	}
	said := strings.Join(words, " ")
	if fromWorkflow {
		return said, true
	}
	if wrapped || !fetches(words, subs) {
		return "", false
	}
	return said, true
}

// unwrap is the command words run, past the variables set for it and what only runs it, and
// whether the firewall is what runs it.
func unwrap(words []string) ([]string, bool) {
	i := slices.IndexFunc(words, func(w string) bool {
		assigns := strings.Contains(w, "=") && !strings.HasPrefix(w, "-")
		return !assigns && w != "exec" && w != "command"
	})
	if i < 0 {
		return nil, false
	}
	words = words[i:]
	for _, firewall := range firewalls {
		if len(words) >= len(firewall) && slices.Equal(words[:len(firewall)], firewall) {
			return words[len(firewall):], true
		}
	}
	return words, false
}

// firewalls are how a command is run through Socket Firewall: the binary itself, this module's
// command, and the command from this repository's own tree.
var firewalls = [][]string{{"sfw"}, {"go", "tool", "sfw"}, {"go", "run", "./cmd/sfw"}}

// fetches says whether a manager's command line reaches its registry.
func fetches(words, subs []string) bool {
	if subs == nil {
		return true
	}
	sub := ""
	for i := 1; i < len(words); i++ {
		w := words[i]
		if slices.Contains(valued, w) {
			i++
			continue
		}
		if strings.HasPrefix(w, "-") {
			continue
		}
		sub = w
		break
	}
	return slices.Contains(subs, sub)
}
