package interrupt

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// started are the files that may start a process with exec.Command, which a cancelled context
// does not stop, and why. Every other process this repository starts is exec.CommandContext's,
// under the context of a main that has Context: a SIGTERM to the command alone, or a Ctrl+C,
// stops it rather than leaving it running after the command is gone.
var started = map[string]string{
	"cmd/versions/refresh.go": "git that commits, switches and pushes the checkout: one killed mid-commit leaves .git/index.lock, and a terminal's Ctrl+C reaches git itself, which cleans up",
	"versions/gate/gate.go":   "Gate.Check is a library's API, called from repositories' tests, with no context to give; its git ls-files is over in milliseconds",
}

func TestEveryProcessStartedIsCancelledWithItsCommand(t *testing.T) {
	root := filepath.Join("..", "..")
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") && rel != "." || rel == "site" || rel == "_output" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Command" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "exec" {
				seen[rel] = true
				if started[rel] == "" {
					t.Errorf("%s: exec.Command, which a cancelled context does not stop: exec.CommandContext with the command's context", fset.Position(sel.Pos()))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for rel := range started {
		if !seen[rel] {
			t.Errorf("%s starts nothing with exec.Command: take it out of started", rel)
		}
	}
}
