// Package ctxlife holds every goroutine to the lifetime of the context it is given.
//
// A function's context parameter is its caller's: it ends when the caller is done with the call,
// and a caller that runs each operation under a deadline it cancels on return ends it as soon as
// the function returns. A goroutine the function starts and does not wait for outlives that - a
// server, a monitor, a watch - and still using the caller's context, it stops working the moment
// the call is over, with nothing to say so: every workspace on a host lost DNS this way, its
// resolver serving under the context of the start that made it.
//
// So a goroutine that uses a context parameter of the function that starts it is one of three
// things, and this asks which:
//
//   - waited for: the function receives from a channel or calls Wait after starting it, so the
//     goroutine is over before the caller's context is;
//   - detached: the parameter was replaced by context.WithoutCancel (or Background) first;
//   - bound on purpose, with `ctx-lifetime: <reason>` on the go statement or the line above it,
//     for a goroutine that should end with the call - one that stops when a request is abandoned.
//
// It reads syntax, not types: a parameter is a context when its type is written context.Context.
package ctxlife

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// Finding is one goroutine that uses its starter's context and outlives the call.
type Finding struct {
	File  string
	Line  int
	Param string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: this goroutine uses %s, its caller's context, and the function returns without waiting for it: "+
		"detach it (context.WithoutCancel), wait for it, or say why it should end with the call in a `ctx-lifetime:` comment",
		f.File, f.Line, f.Param)
}

const exemptMark = "ctx-lifetime:"

// Check is every finding in the .go files under roots, tests left out.
func Check(roots ...string) ([]Finding, error) {
	var out []Finding
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// What a build, a tool or a cache wrote is nobody's code: a dot-directory (an
				// image builder's cache the user cannot read, once), _output, a vendored copy.
				name := d.Name()
				if name == "testdata" || name == "vendor" || name == "_output" || (path != root && strings.HasPrefix(name, ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			found, err := CheckFile(path)
			if err != nil {
				return err
			}
			out = append(out, found...)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// CheckFile is the findings in one file.
func CheckFile(path string) ([]Finding, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	exempt := map[int]bool{}
	for _, group := range file.Comments {
		for _, c := range group.List {
			if strings.Contains(c.Text, exemptMark) {
				exempt[fset.Position(c.Pos()).Line] = true
			}
		}
	}

	var out []Finding
	ast.Inspect(file, func(n ast.Node) bool {
		var (
			params *ast.FieldList
			body   *ast.BlockStmt
		)
		switch fn := n.(type) {
		case *ast.FuncDecl:
			params, body = fn.Type.Params, fn.Body
		case *ast.FuncLit:
			params, body = fn.Type.Params, fn.Body
		default:
			return true
		}
		if body == nil {
			return true
		}
		for _, name := range contextParams(params) {
			for _, g := range goStatements(body) {
				if !uses(g.Call, name) || detachedBefore(body, name, g.Pos()) || waitsAfter(body, g) {
					continue
				}
				line := fset.Position(g.Pos()).Line
				if exempt[line] || exempt[line-1] {
					continue
				}
				out = append(out, Finding{File: path, Line: line, Param: name})
			}
		}
		return true
	})
	return out, nil
}

// contextParams is the names of params written as context.Context.
func contextParams(params *ast.FieldList) []string {
	var out []string
	if params == nil {
		return out
	}
	for _, field := range params.List {
		sel, ok := field.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Context" {
			continue
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "context" {
			continue
		}
		for _, name := range field.Names {
			if name.Name != "_" {
				out = append(out, name.Name)
			}
		}
	}
	return out
}

// goStatements is the go statements of body itself, not of the functions declared inside it:
// those are asked about their own parameters.
func goStatements(body *ast.BlockStmt) []*ast.GoStmt {
	var out []*ast.GoStmt
	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.GoStmt:
			out = append(out, s)
			return false
		}
		return true
	})
	return out
}

// uses is whether call reads name - in its arguments, or in the body of the function it runs -
// where name is not declared again.
func uses(call *ast.CallExpr, name string) bool {
	found := false
	ast.Inspect(call, func(n ast.Node) bool {
		if found {
			return false
		}
		if lit, ok := n.(*ast.FuncLit); ok && declares(lit.Type.Params, name) {
			return false
		}
		// context.WithoutCancel(ctx) keeps what ctx carries and none of its ending.
		if call, ok := n.(*ast.CallExpr); ok && isDetached(call) {
			return false
		}
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return true
	})
	return found
}

func declares(params *ast.FieldList, name string) bool {
	if params == nil {
		return false
	}
	for _, field := range params.List {
		for _, n := range field.Names {
			if n.Name == name {
				return true
			}
		}
	}
	return false
}

// detachedBefore is whether body replaces name with a context that has no cancellation of the
// caller's before pos.
func detachedBefore(body *ast.BlockStmt, name string, pos token.Pos) bool {
	detached := false
	ast.Inspect(body, func(n ast.Node) bool {
		if detached || n == nil || n.Pos() >= pos {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range as.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok || id.Name != name || i >= len(as.Rhs) {
				continue
			}
			if isDetached(as.Rhs[i]) {
				detached = true
			}
		}
		return true
	})
	return detached
}

func isDetached(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "context" {
		return false
	}
	switch sel.Sel.Name {
	case "WithoutCancel", "Background", "TODO":
		return true
	}
	return false
}

// waitsAfter is whether body, after g and outside the function g runs, receives from a channel,
// ranges over one, or calls a Wait: what a function that is done only when its goroutine is does.
func waitsAfter(body *ast.BlockStmt, g *ast.GoStmt) bool {
	waits := false
	ast.Inspect(body, func(n ast.Node) bool {
		if waits || n == nil {
			return false
		}
		if n == g {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if n.Pos() < g.End() {
			return true
		}
		switch x := n.(type) {
		case *ast.UnaryExpr:
			if x.Op == token.ARROW {
				waits = true
			}
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Wait" {
				waits = true
			}
		}
		return true
	})
	return waits
}
