// Package testquality flags tests that cannot fail and tests skipped unconditionally.
// It does not require TestFoo to mention Foo: tests here are named for behaviour.
package testquality

import (
	"errors"
	"go/ast"
	"go/constant"
	"go/types"
	"strings"
	"time"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

var errNoInspector = errors.New("testquality: the inspect analyzer did not run")

var Analyzer = &analysis.Analyzer{
	Name:     "testquality",
	Doc:      "flags tests that assert nothing and tests that are skipped unconditionally",
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

var failMethods = map[string]bool{
	"Error": true, "Errorf": true, "Fatal": true, "Fatalf": true,
	"Fail": true, "FailNow": true,
	"Skip": true, "Skipf": true, "SkipNow": true,
}

var skipMethods = map[string]bool{"Skip": true, "Skipf": true, "SkipNow": true}

// assertionPackages count even when a closure captures t rather than receiving it.
var assertionPackages = map[string]bool{
	"github.com/stretchr/testify/assert":  true,
	"github.com/stretchr/testify/require": true,
	"pgregory.net/rapid":                  true,
}

func run(pass *analysis.Pass) (any, error) {
	insp, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok {
		return nil, errNoInspector
	}

	insp.Preorder([]ast.Node{(*ast.FuncDecl)(nil)}, func(n ast.Node) {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || !isTestFunc(pass, fn) {
			return
		}

		if stmt := unconditionalSkip(pass, fn); stmt != nil {
			pass.Reportf(stmt.Pos(), "%s is skipped unconditionally: it is listed as a test, "+
				"counted by every summary, and nothing about a green run says it did not run. "+
				"Delete it, or guard the skip by a build tag, testing.Short() or a check of "+
				"the environment it needs", fn.Name.Name)
		}

		// Benchmarks are exempt from the assertion rule only: they claim a number.
		if !strings.HasPrefix(fn.Name.Name, "Benchmark") && !canFail(pass, fn) {
			pass.Reportf(fn.Pos(), "%s cannot fail: nothing in it reaches t.Error, t.Fatal, "+
				"t.Skip or an assertion, and no helper is handed the test. A test that asserts "+
				"nothing passes forever and reports this package as covered", fn.Name.Name)
		}

		for _, sleep := range longSleeps(pass, fn) {
			pass.Reportf(sleep.call.Pos(), "%s sleeps %s for nothing it can see: a wait on the clock "+
				"passes on a quiet machine and fails on a loaded one, which is what a flake is. Wait for "+
				"the condition - a channel, a log line, Eventually - or say in the allowlist why the "+
				"clock is the only signal there is", fn.Name.Name, sleep.d)
		}
	})

	return nil, nil
}

// isTestFunc resolves by parameter type as well as name, so TestingSomething helpers are not tests.
func isTestFunc(pass *analysis.Pass, fn *ast.FuncDecl) bool {
	if fn.Recv != nil || fn.Body == nil {
		return false
	}
	switch {
	case strings.HasPrefix(fn.Name.Name, "Test"),
		strings.HasPrefix(fn.Name.Name, "Benchmark"),
		strings.HasPrefix(fn.Name.Name, "Fuzz"):
	default:
		return false
	}
	return testParam(pass, fn) != nil
}

func testParam(pass *analysis.Pass, fn *ast.FuncDecl) types.Object {
	if fn.Type.Params == nil {
		return nil
	}
	for _, field := range fn.Type.Params.List {
		ptr, ok := pass.TypesInfo.TypeOf(field.Type).(*types.Pointer)
		if !ok {
			continue
		}
		named, ok := ptr.Elem().(*types.Named)
		if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != "testing" {
			continue
		}
		switch named.Obj().Name() {
		case "T", "B", "F":
			if len(field.Names) > 0 {
				return pass.TypesInfo.Defs[field.Names[0]]
			}
		}
	}
	return nil
}

// unconditionalSkip looks only at top-level statements: a skip inside an if, loop or closure
// is a legitimate decision about the environment.
func unconditionalSkip(pass *analysis.Pass, fn *ast.FuncDecl) ast.Stmt {
	for _, stmt := range fn.Body.List {
		expr, ok := stmt.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := expr.X.(*ast.CallExpr)
		if !ok {
			continue
		}
		if name, ok := testMethod(pass, call); ok && skipMethods[name] {
			return stmt
		}
	}
	return nil
}

// canFail inspects the whole body, closures and subtests included.
func canFail(pass *analysis.Pass, fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		if name, ok := testMethod(pass, call); ok && failMethods[name] {
			found = true
			return false
		}

		if isFromPackage(pass, call, assertionPackages) {
			found = true
			return false
		}

		// Any helper handed a testing value can fail the test; too many to enumerate.
		for _, arg := range call.Args {
			if isTestingValue(pass, arg) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func testMethod(pass *analysis.Pass, call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	if !isTestingValue(pass, sel.X) {
		return "", false
	}
	return sel.Sel.Name, true
}

func isTestingValue(pass *analysis.Pass, expr ast.Expr) bool {
	ptr, ok := pass.TypesInfo.TypeOf(expr).(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := ptr.Elem().(*types.Named)
	if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != "testing" {
		return false
	}
	switch named.Obj().Name() {
	case "T", "B", "F":
		return true
	}
	return false
}

func isFromPackage(pass *analysis.Pass, call *ast.CallExpr, pkgs map[string]bool) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	fn, ok := pass.TypesInfo.Uses[sel.Sel].(*types.Func)
	if !ok || fn.Pkg() == nil {
		return false
	}
	return pkgs[fn.Pkg().Path()]
}

// longSleep is one time.Sleep in a test's body, closures included, of a second or more.
type longSleep struct {
	call *ast.CallExpr
	d    time.Duration
}

// longSleeps is every time.Sleep in fn whose duration is a constant of a second or more: under a
// second paces a loop, and a duration the test computes is not a number to judge here. A helper's
// sleep is the helper's, not every caller's.
func longSleeps(pass *analysis.Pass, fn *ast.FuncDecl) []longSleep {
	var found []longSleep
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Sleep" || !isFromPackage(pass, call, map[string]bool{"time": true}) {
			return true
		}
		// A duration that is not a constant has no value here (the zero TypeAndValue's is nil).
		value := pass.TypesInfo.Types[call.Args[0]].Value
		if value == nil {
			return true
		}
		ns, exact := constant.Int64Val(constant.ToInt(value))
		if !exact {
			return true
		}
		if ns < int64(time.Second) {
			return true
		}
		found = append(found, longSleep{call: call, d: time.Duration(ns)})
		return true
	})
	return found
}
