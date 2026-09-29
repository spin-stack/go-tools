package testquality

import (
	"fmt"
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
	"golang.org/x/tools/go/packages"
)

// Finding is one test the Analyzer flagged, in a package of a module.
type Finding struct {
	Package string // import path without go/packages' ` [p.test]` and `_test`
	Test    string
	Pos     token.Position
	Message string
}

// Key is what an allowlist line names. It omits the line number, which would break on every
// edit above a test, and the package is the one a person writes, not the one go/packages loaded.
func (f Finding) Key() string { return f.Package + ":" + f.Test }

// tests is the build tags a test of another build is written under, in every spin-stack
// repository: analyzed with them, a test behind one is held like any other.
const tests = "integration,e2e"

// Findings runs the Analyzer over the packages patterns name, their tests included, from the
// working directory's module.
func Findings(patterns ...string) ([]Finding, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedDeps |
			packages.NeedImports | packages.NeedModule,
		Tests:      true,
		BuildFlags: []string{"-tags=" + tests},
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("loading packages: %w", err)
	}

	var findings []Finding
	for _, pkg := range pkgs {
		if len(pkg.Syntax) == 0 {
			continue
		}
		pass := &analysis.Pass{
			Analyzer:  Analyzer,
			Fset:      pkg.Fset,
			Files:     pkg.Syntax,
			Pkg:       pkg.Types,
			TypesInfo: pkg.TypesInfo,
			Report: func(d analysis.Diagnostic) {
				test, _, _ := strings.Cut(d.Message, " ")
				findings = append(findings, Finding{
					Package: packagePath(pkg.PkgPath),
					Test:    test,
					Pos:     pkg.Fset.Position(d.Pos),
					Message: d.Message,
				})
			},
			ResultOf: map[*analysis.Analyzer]any{},
		}
		// Supplied directly: singlechecker has nowhere to put the allowlist.
		pass.ResultOf[inspect.Analyzer] = inspector.New(pkg.Syntax)
		if _, err := Analyzer.Run(pass); err != nil {
			return nil, fmt.Errorf("%s: %w", pkg.PkgPath, err)
		}
	}
	return findings, nil
}

func packagePath(pkgPath string) string {
	if base, _, ok := strings.Cut(pkgPath, " ["); ok {
		pkgPath = base
	}
	return strings.TrimSuffix(pkgPath, "_test")
}
