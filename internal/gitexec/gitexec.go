// Package gitexec holds this repository's tests to running git through internal/gittest.
//
// git looks for its repository upward from where it runs, and a test runs inside the checkout,
// so a git a test meant for a repository it made can land on the checkout's own config - which
// every worktree and every agent commits from. In spin that put a test's author on commits
// pushed to GitHub, twice. internal/gittest is the fence; this is what makes it the only way,
// decided from the syntax:
//
//   - a test file that execs git itself, rather than through gittest.Command;
//   - a package whose own code execs git, with tests and no gittest.Fence() in them: its tests
//     run that git, and one that loses its -C - a mutant mutate makes - finds the checkout.
//
// What it sees is os/exec's Command or CommandContext with the program spelled "git". A wrapper
// that takes the program as an argument is not seen.
package gitexec

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
)

// Finding is one git a test can run unfenced, as a line to go to. Key is what an allow line
// names: the test file, or the package's directory.
type Finding struct {
	File string
	Line int
	Key  string
	Why  string
}

func (f Finding) String() string { return fmt.Sprintf("%s:%d: %s", f.File, f.Line, f.Why) }

// fence is the package whose Command and Fence are the way a test runs git.
const fence = "internal/gittest"

type pkg struct {
	runs   []Finding // the package's own code's execs of git
	tested []Finding // its test files' own execs of git
	tests  bool
	fenced bool
}

// Check walks root as go walks a module - no directory whose name starts with . or _, no
// testdata - and reports every git a test can run unfenced, in the order of the tree.
func Check(root string) ([]Finding, error) {
	pkgs := &tree{byDir: map[string]*pkg{}}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		return read(root, path, pkgs)
	})
	if err != nil {
		return nil, err
	}
	var found []Finding
	for _, p := range pkgs.inOrder {
		found = append(found, p.tested...)
		if p.tests && !p.fenced {
			found = append(found, p.runs...)
		}
	}
	return found, nil
}

// tree is the packages read, by directory and in the order the walk met them - which is the
// order their findings are said in, the same on every run.
type tree struct {
	byDir   map[string]*pkg
	inOrder []*pkg
}

func (t *tree) of(dir string) *pkg {
	p := t.byDir[dir]
	if p == nil {
		p = &pkg{}
		t.byDir[dir] = p
		t.inOrder = append(t.inOrder, p)
	}
	return p
}

// read adds the Go file at path to its package in pkgs: its execs of git, and whether it is a
// test and calls gittest.Fence.
func read(root, path string, pkgs *tree) error {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == fence {
		return nil
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return err
	}
	p := pkgs.of(dir)
	test := strings.HasSuffix(path, "_test.go")
	for _, line := range gitExecs(fset, file) {
		f := Finding{File: filepath.ToSlash(rel), Line: line}
		if test {
			f.Key, f.Why = f.File, "a test runs git itself: run it through gittest.Command (internal/gittest), "+
				"which keeps git off any repository the test did not make"
			p.tested = append(p.tested, f)
		} else {
			f.Key, f.Why = dir, "this package's code runs git and its tests do not fence it: call gittest.Fence() "+
				"first in its TestMain (internal/gittest), or a git of it that misses its -C writes the checkout's config"
			p.runs = append(p.runs, f)
		}
	}
	if test {
		p.tests = true
		p.fenced = p.fenced || callsFence(file)
	}
	return nil
}

// programArg is which argument of os/exec's constructors names the program.
var programArg = map[string]int{"Command": 0, "CommandContext": 1}

// gitExecs is the line of every os/exec Command or CommandContext in file whose program is "git".
func gitExecs(fset *token.FileSet, file *ast.File) []int {
	name := ""
	for _, imp := range file.Imports {
		if imp.Path.Value == `"os/exec"` {
			name = "exec"
			if imp.Name != nil {
				name = imp.Name.Name
			}
		}
	}
	if name == "" {
		return nil
	}
	var lines []int
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); !ok || x.Name != name {
			return true
		}
		// Both take the program as a parameter of their own, so code that builds has the argument.
		program, ok := programArg[sel.Sel.Name]
		if !ok {
			return true
		}
		if lit, ok := call.Args[program].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil && s == "git" {
				lines = append(lines, fset.Position(call.Pos()).Line)
			}
		}
		return true
	})
	return lines
}

// callsFence is whether file calls gittest.Fence.
func callsFence(file *ast.File) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Fence" {
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == "gittest" {
					found = true
				}
			}
		}
		return !found
	})
	return found
}
