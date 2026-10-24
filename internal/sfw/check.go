package sfw

import (
	"bytes"
	"encoding/json"
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

	"github.com/spin-stack/go-tools/internal/shell"
)

// Finding is one thing that breaks the rule, where it is written.
type Finding struct {
	File    string
	Line    int
	Command string // the command, or the file's own name for what is not one
	Why     Why
}

// Why is which rule a finding breaks.
type Why int

const (
	// Unfirewalled is a pnpm command that fetches packages without Socket Firewall.
	Unfirewalled Why = iota
	// NotPnpm is npm, npx, yarn or bun: pnpm is the one package manager, so there is one lockfile
	// and one set of install rules (pnpm-workspace.yaml's) to hold.
	NotPnpm
	// Lockfile is another package manager's lockfile, which pnpm neither reads nor writes.
	Lockfile
	// Unpinned is a package.json that does not name pnpm, at a version, as its packageManager.
	Unpinned
)

func (f Finding) String() string {
	switch f.Why {
	case NotPnpm:
		return fmt.Sprintf("%s:%d: %q: pnpm is the package manager here, never npm, npx, yarn or bun: run it with pnpm (pnpm dlx for npx), through `go tool sfw` when it fetches", f.File, f.Line, f.Command)
	case Lockfile:
		return fmt.Sprintf("%s: another package manager's lockfile: `pnpm import` reads it into pnpm-lock.yaml, and then it goes", f.File)
	case Unpinned:
		return fmt.Sprintf("%s: no \"packageManager\": \"pnpm@<version>\": every install of it is to be pnpm, at the version named here", f.File)
	default:
		return fmt.Sprintf("%s:%d: %q fetches packages without Socket Firewall: run it as `go tool sfw %s`", f.File, f.Line, f.Command, f.Command)
	}
}

// fetching are pnpm's subcommands that reach the registry. One that only runs what is installed -
// build, run, test, exec - is left alone.
var fetching = []string{"install", "i", "add", "update", "up", "upgrade", "dlx", "create", "fetch", "import"}

// others are the package managers that are not pnpm, and pnpx, which is pnpm dlx.
var others = []string{"npm", "npx", "yarn", "bun", "bunx"}

// valued are the flags before a subcommand that take the word after them: `pnpm -C dashboard
// install` installs.
var valued = []string{"-C", "--dir", "--prefix", "--cwd", "-F", "--filter", "--workspace"}

// Check is every finding in the files git lists under root - tracked, or untracked and not
// ignored: the commands workflows and composite actions, Taskfiles, shell scripts and Dockerfiles
// run, and the package.json files and lockfiles themselves. pnpm is the one package manager, and
// what it fetches it fetches through the firewall, wherever that is written.
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
		if kind == lockfile {
			findings = append(findings, Finding{File: rel, Command: path.Base(rel), Why: Lockfile})
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			if os.IsNotExist(err) {
				continue // deleted, not yet staged
			}
			return nil, fmt.Errorf("sfw: %w", err)
		}
		if kind == manifest {
			var pkg struct {
				PackageManager string `json:"packageManager"`
			}
			if err := json.Unmarshal(raw, &pkg); err != nil {
				return nil, fmt.Errorf("sfw: %s: %w", rel, err)
			}
			if !strings.HasPrefix(pkg.PackageManager, "pnpm@") {
				findings = append(findings, Finding{File: rel, Command: path.Base(rel), Why: Unpinned})
			}
			continue
		}
		lines, err := commands(kind, raw)
		if err != nil {
			return nil, fmt.Errorf("sfw: %s: %w", rel, err)
		}
		for _, l := range lines {
			for _, cmd := range segments(l.Text) {
				if f, ok := judge(cmd); ok {
					f.File, f.Line = rel, l.Line
					findings = append(findings, f)
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
	manifest
	lockfile
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
	case base == "package.json":
		return manifest
	case base == "package-lock.json", base == "npm-shrinkwrap.json", base == "yarn.lock", base == "bun.lockb", base == "bun.lock":
		return lockfile
	}
	return none
}

// commands is the shell a file runs: a workflow's and a Taskfile's are the strings under run,
// cmd, sh and cmds, and not a description that names a command; a script's is the whole file.
func commands(k kind, raw []byte) ([]shell.Line, error) {
	if k == script {
		return shell.Lines(string(raw), 1), nil
	}
	var doc yaml.Node
	if err := yaml.NewDecoder(bytes.NewReader(raw)).Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil // an empty file runs nothing
		}
		return nil, err
	}
	var out []shell.Line
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
				out = append(out, shell.Lines(n.Value, first)...)
			}
		case yaml.AliasNode:
		}
	}
	walk(&doc, "")
	return out, nil
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

// judge is the finding cmd makes, without where it is written: any package manager but pnpm,
// run through the firewall or not, and a pnpm command that fetches and is not run through it.
func judge(cmd string) (Finding, bool) {
	words, wrapped := unwrap(strings.Fields(cmd))
	if len(words) == 0 {
		return Finding{}, false
	}
	said := strings.Join(words, " ")
	switch {
	case slices.Contains(others, words[0]):
		return Finding{Command: said, Why: NotPnpm}, true
	case !wrapped && (words[0] == "pnpx" || words[0] == "pnpm" && fetches(words)):
		return Finding{Command: said, Why: Unfirewalled}, true
	}
	return Finding{}, false
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

// fetches says whether a pnpm command line reaches the registry.
func fetches(words []string) bool {
	for i := 1; i < len(words); i++ {
		w := words[i]
		if slices.Contains(valued, w) {
			i++
			continue
		}
		if strings.HasPrefix(w, "-") {
			continue
		}
		return slices.Contains(fetching, w)
	}
	return false
}
