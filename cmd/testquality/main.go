// Command testquality runs the testquality analyzer against a repository's allowlist of the
// findings it keeps on purpose (-allow; none by default), which is a ratchet: an entry nothing
// matches fails too.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
	"golang.org/x/tools/go/packages"

	"github.com/spin-stack/go-tools/internal/allow"
	"github.com/spin-stack/go-tools/internal/testquality"
)

func main() {
	allowPath := flag.String("allow", "", "file of allowed exemptions, one key per line with the reason")
	tags := flag.String("tags", "integration,e2e", "build tags to analyze under")
	flag.Parse()

	patterns := flag.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}

	allowed, err := allow.Read(*allowPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "testquality: %v\n", err)
		os.Exit(2)
	}

	findings, err := analyze(patterns, *tags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(2)
	}

	var unexplained []string
	used := map[string]bool{}
	for _, f := range findings {
		if allowed[f.key] {
			used[f.key] = true
			continue
		}
		unexplained = append(unexplained, fmt.Sprintf("%s: %s", f.pos, f.message))
	}

	// Stale exemptions fail too, or the list stops shrinking.
	stale := allow.Stale(allowed, used)
	sort.Strings(unexplained)

	for _, u := range unexplained {
		fmt.Fprintln(os.Stderr, u)
	}
	for _, s := range stale {
		fmt.Fprintf(os.Stderr, "%s is in %s and is no longer a finding: remove the line\n", s, *allowPath)
	}
	if len(unexplained) > 0 || len(stale) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d test(s) that cannot fail or never run, %d stale exemption(s)\n",
			len(unexplained), len(stale))
		os.Exit(1)
	}
	fmt.Printf("testquality: %d exemption(s), no new findings\n", len(allowed))
}

type finding struct {
	key     string // package path + test name, which is what an allow-list line names
	pos     string
	message string
}

func analyze(patterns []string, tags string) ([]finding, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedDeps |
			packages.NeedImports | packages.NeedModule,
		Tests:      true,
		BuildFlags: []string{"-tags=" + tags},
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("loading packages: %w", err)
	}

	var findings []finding
	for _, pkg := range pkgs {
		if len(pkg.Syntax) == 0 {
			continue
		}
		pass := &analysis.Pass{
			Analyzer:  testquality.Analyzer,
			Fset:      pkg.Fset,
			Files:     pkg.Syntax,
			Pkg:       pkg.Types,
			TypesInfo: pkg.TypesInfo,
			Report: func(d analysis.Diagnostic) {
				pos := pkg.Fset.Position(d.Pos)
				findings = append(findings, finding{
					key:     key(pkg.PkgPath, d.Message),
					pos:     relative(pos.Filename) + ":" + fmt.Sprint(pos.Line),
					message: d.Message,
				})
			},
			ResultOf: map[*analysis.Analyzer]any{},
		}
		// Supplied directly: singlechecker has nowhere to put the allow-list.
		pass.ResultOf[inspect.Analyzer] = inspector.New(pkg.Syntax)
		if _, err := testquality.Analyzer.Run(pass); err != nil {
			return nil, fmt.Errorf("%s: %w", pkg.PkgPath, err)
		}
	}
	return findings, nil
}

// key omits the line number, which would break on every edit above a test.
func key(pkgPath, message string) string {
	name, _, _ := strings.Cut(message, " ")
	return packageName(pkgPath) + ":" + name
}

// packageName strips go/packages' ` [p.test]` and `_test` so allow-list lines are writable by hand.
func packageName(pkgPath string) string {
	if base, _, ok := strings.Cut(pkgPath, " ["); ok {
		pkgPath = base
	}
	return strings.TrimSuffix(pkgPath, "_test")
}

func relative(path string) string {
	wd, err := os.Getwd()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(wd, path)
	if err != nil {
		return path
	}
	return rel
}
