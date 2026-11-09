package linkweight

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// Std is the module the standard library's packages are counted under.
const Std = "std"

// Package is one package of a binary as `go list -deps -json` says it.
type Package struct {
	ImportPath string   `json:"ImportPath"`
	Imports    []string `json:"Imports"`
	Standard   bool     `json:"Standard"`
	Module     *struct {
		Path string `json:"Path"`
	} `json:"Module"`
}

// module is the module a package is counted under.
func (p Package) module() string {
	if p.Module == nil || p.Standard {
		return Std
	}
	return p.Module.Path
}

// ParsePackages reads `go list -deps -json`'s stream of objects.
func ParsePackages(r io.Reader) ([]Package, error) {
	var out []Package
	d := json.NewDecoder(r)
	for {
		var p Package
		err := d.Decode(&p)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading go list's packages: %w", err)
		}
		out = append(out, p)
	}
}

// Weight is a module's or a package's bytes in the binary.
type Weight struct {
	Name  string `json:"name"`
	Bytes uint64 `json:"bytes"`
}

// Finding is something the binary reaches that a list may have to explain: a capability, or a
// function that turns the linker's method elimination off. Chain is the packages the linker went
// through to reach Symbol, from the entry point.
type Finding struct {
	Kind   string   `json:"kind"`
	Name   string   `json:"name"`
	Symbol string   `json:"symbol"`
	Chain  []string `json:"chain"`
	// ViaMethod is a chain that enters a method through its receiver's type: for a breaker, it
	// may be linked only because another breaker turned method elimination off.
	ViaMethod bool `json:"via_method,omitempty"`
}

// Kinds of Finding.
const (
	KindCapability    = "capability"
	KindReflectMethod = "reflect-method"
)

// Cut is one import a package of the repository makes, and what the binary would stop linking
// without it: the bytes of every package it then no longer reaches, and the modules it no longer
// links at all.
type Cut struct {
	From    string   `json:"from"`
	To      string   `json:"to"`
	Bytes   uint64   `json:"bytes"`
	Modules []string `json:"modules,omitempty"`
}

// Binary is what one main package links.
type Binary struct {
	Root string `json:"root"`
	// Shipped is the bytes loaded at run time, the size of the stripped file. A module's and a
	// package's weight is its symbols and its share of .gopclntab, in proportion to its machine
	// code; Unattributed is the part no package owns by name (string data, funcdata).
	Shipped      uint64   `json:"shipped"`
	Unattributed uint64   `json:"unattributed"`
	Modules      []Weight `json:"modules"`
	Packages     []Weight `json:"packages"`
	// MethodElimination is whether the linker could drop the methods nothing calls. Off, every
	// exported method of every reachable type is in the binary, and Breakers say why.
	MethodElimination bool      `json:"method_elimination"`
	Breakers          []Finding `json:"breakers,omitempty"`
	Capabilities      []Finding `json:"capabilities,omitempty"`
	Cuts              []Cut     `json:"cuts,omitempty"`
}

// Input is what Analyze reads for one binary.
type Input struct {
	// Root is the main package as the report names it (cmd/boot); MainPath its import path.
	Root, MainPath string
	// Module is the repository's module path: an import a package of it makes is a cut it can make.
	Module   string
	Packages []Package
	Layout   Layout
	Graph    *Graph
}

// Analyze is the report for one binary.
func Analyze(in Input) Binary {
	b := Binary{Root: in.Root, Shipped: in.Layout.Shipped}
	known := map[string]bool{}
	moduleOf := map[string]string{}
	for _, p := range in.Packages {
		known[p.ImportPath] = true
		moduleOf[p.ImportPath] = p.module()
	}
	pkgOf := func(sym string) string { return PackageOf(sym, known, in.MainPath) }

	pkgBytes := map[string]uint64{}
	code := map[string]uint64{}
	var allCode uint64
	for _, s := range in.Layout.Symbols {
		p := pkgOf(s.Name)
		if s.Code {
			code[p] += s.Size
			allCode += s.Size
		}
		pkgBytes[p] += s.Size
	}
	if allCode > 0 {
		for p, c := range code {
			pkgBytes[p] += uint64(float64(in.Layout.PCLNTab) * float64(c) / float64(allCode))
		}
	}
	b.Unattributed = pkgBytes[Unattributed]
	delete(pkgBytes, Unattributed)
	modBytes := map[string]uint64{}
	for p, n := range pkgBytes {
		modBytes[moduleOf[p]] += n
	}
	b.Packages = sorted(pkgBytes)
	b.Modules = sorted(modBytes)

	path := importPaths(in.Packages)
	chain := func(sym string) ([]string, bool) {
		c, via := in.Graph.Chain(sym)
		pc := packageChain(c, pkgOf)
		// A chain that starts in a package's init says nothing of who imports the package (the
		// linker runs every init it links), and a call the compiler inlined skips the package it
		// was written in. The imports are what a refactor cuts, so each gap is filled with them.
		full := []string{in.MainPath}
		for _, p := range pc {
			if p == full[len(full)-1] {
				continue
			}
			full = append(full, path(full[len(full)-1], p)[1:]...)
		}
		for i := range full {
			full[i] = strings.TrimPrefix(full[i], in.Module+"/")
		}
		return full, via
	}
	b.MethodElimination, b.Breakers = breakers(in.Graph, pkgOf, chain)
	b.Capabilities = capabilities(in.Graph, in.Layout.Symbols, chain)
	b.Cuts = cuts(in.MainPath, in.Module, in.Packages, pkgBytes)
	return b
}

func sorted(m map[string]uint64) []Weight {
	out := make([]Weight, 0, len(m))
	for k, v := range m {
		out = append(out, Weight{k, v})
	}
	slices.SortFunc(out, func(a, b Weight) int {
		return cmp.Or(cmp.Compare(b.Bytes, a.Bytes), cmp.Compare(a.Name, b.Name))
	})
	return out
}

// packageChain is a symbol chain as the packages it passes through, each once in a row: a chain
// of symbols is runtime plumbing a reader does not act on, the packages are where an import can
// be cut.
func packageChain(syms []string, pkgOf func(string) string) []string {
	var out []string
	for _, s := range syms {
		p := pkgOf(s)
		// The runtime starts every chain; it is not an import anybody cuts.
		if p == Unattributed || (len(out) == 0 && p == "runtime") {
			continue
		}
		if len(out) == 0 || out[len(out)-1] != p {
			out = append(out, p)
		}
	}
	return out
}

// importPaths answers, for two packages, a shortest chain of imports from the first to the
// second, both ends included; the two alone when the first does not import the second at all.
func importPaths(pkgs []Package) func(from, to string) []string {
	byPath := map[string]Package{}
	for _, p := range pkgs {
		byPath[p.ImportPath] = p
	}
	parents := map[string]map[string]string{}
	return func(from, to string) []string {
		parent, ok := parents[from]
		if !ok {
			parent = map[string]string{from: ""}
			queue := []string{from}
			for len(queue) > 0 {
				n := queue[0]
				queue = queue[1:]
				for _, m := range byPath[n].Imports {
					if _, seen := parent[m]; !seen {
						parent[m] = n
						queue = append(queue, m)
					}
				}
			}
			parents[from] = parent
		}
		if _, ok := parent[to]; !ok {
			return []string{from, to}
		}
		var out []string
		for c := to; c != ""; c = parent[c] {
			out = append(out, c)
		}
		slices.Reverse(out)
		return out
	}
}

// breakers is whether the linker eliminated unused methods, and the functions that stopped it,
// one per package: the package is what a change removes, and a package marks several functions
// for one reason. Package reflect's own functions are marked when a caller reaches them, and are
// named only when nothing outside reflect is.
func breakers(g *Graph, pkgOf func(string) string, chain func(string) ([]string, bool)) (bool, []Finding) {
	byPkg := map[string]string{}
	var reflectOwn []string
	for _, s := range g.ReflectMethod() {
		// A type symbol carries the mark for its methods; the function that calls is what to cut.
		if strings.HasPrefix(s, "type:") {
			continue
		}
		p := pkgOf(s)
		if p == "reflect" || p == "internal/reflectlite" {
			reflectOwn = append(reflectOwn, s)
			continue
		}
		if cur, ok := byPkg[p]; !ok || s < cur {
			byPkg[p] = s
		}
	}
	if len(byPkg) == 0 {
		for _, s := range reflectOwn {
			if cur, ok := byPkg["reflect"]; !ok || s < cur {
				byPkg["reflect"] = s
			}
		}
	}
	var out []Finding
	for p, s := range byPkg {
		c, via := chain(s)
		out = append(out, Finding{Kind: KindReflectMethod, Name: p, Symbol: s, Chain: c, ViaMethod: via})
	}
	slices.SortFunc(out, func(a, b Finding) int { return cmp.Compare(a.Name, b.Name) })
	return len(out) == 0, out
}

// Capability is something a binary can do that its own description should say: each of Symbols
// linked is the capability reached. A name ending in "*" is a prefix, for a function the
// standard library spells several ways (listenTCP, listenUDP, listenUnix).
type Capability struct {
	Name    string
	Symbols []string
}

// Capabilities is what linkweight reports a binary can reach, Linux's names: the linker graph is
// of the GOOS the build ran for.
var Capabilities = []Capability{
	{"exec", []string{"os.StartProcess", "syscall.forkExec"}},
	{"exec-replace", []string{"syscall.Exec", "golang.org/x/sys/unix.Exec"}},
	{"listen", []string{"net.(*sysListener).*"}},
	{"dial", []string{"net.(*sysDialer).*"}},
	{"http-server", []string{"net/http.(*Server).Serve"}},
	{"mount", []string{"syscall.Mount", "golang.org/x/sys/unix.Mount", "golang.org/x/sys/unix.Fsmount", "golang.org/x/sys/unix.MoveMount"}},
	{"setuid", []string{"syscall.Setuid", "syscall.Setgid", "syscall.Setresuid", "syscall.Setresgid", "golang.org/x/sys/unix.Setuid", "golang.org/x/sys/unix.Setgid", "golang.org/x/sys/unix.Setresuid", "golang.org/x/sys/unix.Setresgid"}},
	{"namespaces", []string{"syscall.Unshare", "golang.org/x/sys/unix.Unshare", "golang.org/x/sys/unix.Setns"}},
	{"ptrace", []string{"syscall.PtraceAttach", "golang.org/x/sys/unix.PtraceAttach", "golang.org/x/sys/unix.PtraceSeize"}},
	{"reboot", []string{"syscall.Reboot", "golang.org/x/sys/unix.Reboot"}},
	{"kernel-module", []string{"golang.org/x/sys/unix.InitModule", "golang.org/x/sys/unix.FinitModule", "golang.org/x/sys/unix.DeleteModule"}},
	{"plugin", []string{"plugin.Open"}},
	{"reflect-call", []string{"reflect.Value.Call", "reflect.Value.CallSlice"}},
}

// cgoSymbol is in a binary exactly when cgo built it: the C side of the runtime's start.
const cgoSymbol = "x_cgo_init"

// capabilities is every capability the binary links, each with the first of its symbols found and
// the chain to it; cgo from the symbol table, since C is not in the Go linker's graph.
func capabilities(g *Graph, syms []Sym, chain func(string) ([]string, bool)) []Finding {
	var out []Finding
	for _, c := range Capabilities {
		if s, ok := linked(g, c.Symbols); ok {
			ch, via := chain(s)
			out = append(out, Finding{Kind: KindCapability, Name: c.Name, Symbol: s, Chain: ch, ViaMethod: via})
		}
	}
	if slices.ContainsFunc(syms, func(s Sym) bool { return s.Name == cgoSymbol }) {
		out = append(out, Finding{Kind: KindCapability, Name: "cgo", Symbol: cgoSymbol})
	}
	slices.SortFunc(out, func(a, b Finding) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// linked is the first of names the graph has, a prefix entry matching the smallest symbol it
// prefixes so the answer does not depend on the order the linker printed.
func linked(g *Graph, names []string) (string, bool) {
	for _, n := range names {
		if prefix, ok := strings.CutSuffix(n, "*"); ok {
			best := ""
			for _, s := range g.Symbols() {
				if strings.HasPrefix(s, prefix) && (best == "" || s < best) {
					best = s
				}
			}
			if best != "" {
				return best, true
			}
			continue
		}
		if g.Has(n) {
			return n, true
		}
	}
	return "", false
}

// MinCut is the least a cut is reported for: below it, an import of fmt or os, which no refactor
// is for.
const MinCut = 64 << 10

// cuts is, for every import a package of module makes, what the binary would stop linking
// without it: the packages no longer reachable from the main package, their bytes, and the
// modules none of whose packages are left. A package-level estimate: a smaller import graph lets
// the linker drop more than the packages it drops (a method-elimination breaker among them turns
// it back on), and less when the cut package's code is still needed and moves elsewhere.
func cuts(mainPath, module string, pkgs []Package, bytes map[string]uint64) []Cut {
	g, ok := newImportGraph(mainPath, pkgs)
	if !ok {
		return nil
	}
	all := g.reach(-1, -1)
	before := g.modules(all, bytes)
	var out []Cut
	for i, p := range pkgs {
		if p.ImportPath != module && !strings.HasPrefix(p.ImportPath, module+"/") {
			continue
		}
		for _, j := range g.succ[i] {
			seen := g.reach(i, j)
			c := Cut{From: strings.TrimPrefix(p.ImportPath, module+"/"), To: strings.TrimPrefix(pkgs[j].ImportPath, module+"/")}
			for k, q := range pkgs {
				if all[k] && !seen[k] {
					c.Bytes += bytes[q.ImportPath]
				}
			}
			if c.Bytes < MinCut {
				continue
			}
			after := g.modules(seen, bytes)
			for m := range before {
				if !after[m] {
					c.Modules = append(c.Modules, m)
				}
			}
			slices.Sort(c.Modules)
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b Cut) int {
		return cmp.Or(cmp.Compare(b.Bytes, a.Bytes), cmp.Compare(a.From, b.From), cmp.Compare(a.To, b.To))
	})
	return out
}

// importGraph is a binary's packages and their imports, by index, from the main package.
type importGraph struct {
	pkgs  []Package
	succ  [][]int
	start int
}

func newImportGraph(mainPath string, pkgs []Package) (importGraph, bool) {
	idx := map[string]int{}
	for i, p := range pkgs {
		idx[p.ImportPath] = i
	}
	start, ok := idx[mainPath]
	g := importGraph{pkgs: pkgs, succ: make([][]int, len(pkgs)), start: start}
	for i, p := range pkgs {
		for _, imp := range p.Imports {
			if j, ok := idx[imp]; ok {
				g.succ[i] = append(g.succ[i], j)
			}
		}
	}
	return g, ok
}

// reach is which packages the main package reaches without the import skipFrom > skipTo.
func (g importGraph) reach(skipFrom, skipTo int) []bool {
	seen := make([]bool, len(g.pkgs))
	seen[g.start] = true
	queue := []int{g.start}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, m := range g.succ[n] {
			if seen[m] || (n == skipFrom && m == skipTo) {
				continue
			}
			seen[m] = true
			queue = append(queue, m)
		}
	}
	return seen
}

// modules is the modules of the packages seen, counting a module when it puts bytes in the binary:
// go list names packages the linker then drops whole.
func (g importGraph) modules(seen []bool, bytes map[string]uint64) map[string]bool {
	left := map[string]bool{}
	for i, p := range g.pkgs {
		if seen[i] && bytes[p.ImportPath] > 0 {
			left[p.module()] = true
		}
	}
	return left
}
