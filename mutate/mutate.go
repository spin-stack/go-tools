// Package mutate breaks the code a change touched and asks the tests whether they notice.
//
// A test is only worth what it refuses. Eight in this tree asserted things that held however
// the code was written - a value compared with itself, a length against the constant the code
// sizes itself with, a UTF-8 check on text that was ASCII - and every one of them passed for as
// long as it existed, reporting its package covered. They were found by breaking the code and
// watching them stay green, which is the only check that reads a test by what it would refuse
// rather than by what it says.
//
// So that is the gate, pointed at the lines a change touched rather than at the tree: for each
// function the diff reaches, small edits that change what it does - a comparison inverted, a
// field no longer written, a bound moved by one - and the package's own tests run against each.
// A mutation no test refuses is either a behaviour nothing holds or an edit that means nothing;
// the first is the finding, and the second is what `mutate-exempt:` on the line is for.
//
// It does not prove a test suite good. A surviving mutation is a question, and the answer is
// either a test or a reason.
package mutate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Mutation is one edit to one file: the bytes to put in place of the bytes that are there.
type Mutation struct {
	File  string
	Line  int
	Func  string
	What  string // what the edit does, as a person reads it
	Start int    // byte offsets into the file as it is on disk
	End   int
	With  string
	// ID is the mutation's number in a run, from 1, which switches it on in its package's schema
	// (Schemata). The caller numbers them.
	ID int
	// site is where the edit is built in beside the rest of its package's, and role which of the
	// site's edits it is; nil where it can only be built alone, through Overlay.
	site *site
	role role
}

func (m Mutation) String() string {
	return fmt.Sprintf("%s:%d (%s): %s", m.File, m.Line, m.Func, m.What)
}

// Changed is the lines a diff against base touches, by file: the Go files that are not tests,
// because a mutation of a test is a question about nothing.
func Changed(base string) (map[string]map[int]bool, error) {
	out, err := exec.Command("git", "diff", "--unified=0", "--no-color", base, "--", "*.go").Output()
	if err != nil {
		return nil, fmt.Errorf("reading the diff against %s: %w", base, err)
	}
	changed := map[string]map[int]bool{}
	file := ""
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			file = strings.TrimPrefix(line, "+++ b/")
			if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
				file = ""
			}
		case strings.HasPrefix(line, "@@") && file != "":
			from, count, ok := hunk(line)
			if !ok {
				continue
			}
			if changed[file] == nil {
				changed[file] = map[int]bool{}
			}
			for i := range count {
				changed[file][from+i] = true
			}
		}
	}
	// A file nothing has yet been committed of is not in a diff, and is all new code: locally
	// that is most of what a change is before it becomes a commit.
	untracked, err := exec.Command("git", "ls-files", "--others", "--exclude-standard", "--", "*.go").Output()
	if err != nil {
		return nil, fmt.Errorf("reading what is not committed yet: %w", err)
	}
	for _, path := range strings.Fields(string(untracked)) {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		lines, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		whole := map[int]bool{}
		for i := range strings.Count(string(lines), "\n") + 1 {
			whole[i+1] = true
		}
		changed[path] = whole
	}
	return changed, nil
}

// hunk is the range of the new file a hunk header names: @@ -old,n +new,m @@.
func hunk(header string) (int, int, bool) {
	fields := strings.Fields(header)
	if len(fields) < 3 || !strings.HasPrefix(fields[2], "+") { // mutate-exempt: git's header is four fields, `@@ -a +b @@`, so < 4 refuses nothing more
		return 0, 0, false
	}
	from, count := strings.TrimPrefix(fields[2], "+"), "1"
	if before, after, ok := strings.Cut(from, ","); ok {
		from, count = before, after
	}
	f, err := strconv.Atoi(from)
	if err != nil {
		return 0, 0, false
	}
	c, err := strconv.Atoi(count)
	if err != nil {
		return 0, 0, false
	}
	return f, c, true
}

// swaps are the operators each becomes: an edit that changes what the code decides, and not one
// that only changes how it says it.
var swaps = map[token.Token]token.Token{
	token.EQL: token.NEQ, token.NEQ: token.EQL,
	token.LSS: token.GEQ, token.GTR: token.LEQ,
	token.LEQ: token.GTR, token.GEQ: token.LSS,
	token.LAND: token.LOR, token.LOR: token.LAND,
	token.ADD: token.SUB, token.SUB: token.ADD,
}

// Plan is every mutation of the functions in one file that the changed lines reach.
func Plan(path string, changed map[int]bool) ([]Mutation, error) {
	return plan(path, changed, true)
}

// StaleExempts is the first line of every mutate-exempt reason in path that covers no edit this
// tool would make there: one over a line it never breaks, or in a function a lane holds, or left
// behind by code that moved. A reason is an answer to an edit; with no edit it answers nothing,
// and it stays in the tree reading as if it did.
func StaleExempts(path string) ([]int, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, source, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	edits, err := plan(path, nil, false)
	if err != nil {
		return nil, err
	}
	edited := map[int]bool{}
	for _, m := range edits {
		edited[m.Line] = true
	}
	var stale []int
	for _, group := range file.Comments {
		if !isExempt(group) {
			continue
		}
		// What Plan leaves alone for this reason: its own lines and the line under each.
		covers := false
		for _, c := range group.List {
			at := fset.Position(c.Pos()).Line
			covers = covers || edited[at] || edited[at+1]
		}
		if !covers {
			stale = append(stale, fset.Position(group.Pos()).Line)
		}
	}
	return stale, nil
}

// isExempt is a comment one of whose lines is a reason: it starts with the marker. Prose that
// only mentions it - this package's own - is not one.
func isExempt(group *ast.CommentGroup) bool {
	for _, c := range group.List {
		if strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(c.Text, "//")), "mutate-exempt:") {
			return true
		}
	}
	return false
}

func plan(path string, changed map[int]bool, honourExempt bool) ([]Mutation, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, source, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	// Generated code is its generator's: a change to it is a change to a query or a schema,
	// whose own tests are where it is asked about, and an edit here is one nothing may make.
	if ast.IsGenerated(file) {
		return nil, nil
	}
	exempt := map[int]bool{}
	if honourExempt {
		exempt = exemptLines(fset, file)
	}
	line := func(n ast.Node) int { return fset.Position(n.Pos()).Line }

	var out []Mutation
	add := func(name string, m edit) {
		at := line(m.node)
		if exempt[at] || exempt[at-1] {
			return
		}
		out = append(out, Mutation{
			File: path, Line: at, Func: name, What: m.what,
			Start: fset.Position(m.node.Pos()).Offset, End: fset.Position(m.node.End()).Offset, With: m.with,
			site: m.site, role: m.role,
		})
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || !touches(fset, fn, changed) {
			continue
		}
		name := fn.Name.Name
		held, err := heldByLane(path, fn)
		if err != nil {
			return nil, err
		}
		if held {
			continue
		}
		sizes, logged := meaningless(fn.Body)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			for _, m := range edits(n, source, fset) {
				if sizes[m.node] || within(m.node, logged) {
					continue
				}
				add(name, m)
			}
			return true
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out, nil
}

// meaningless is what, in one function, an edit would change nothing a caller could observe by:
// literals that only size something, and the conditions of branches that only log. Each was
// answered line by line with a mutate-exempt saying so - a fifth of every one in the tree - so
// they are not made at all.
func meaningless(body *ast.BlockStmt) (map[ast.Node]bool, []ast.Expr) {
	sizes := map[ast.Node]bool{}
	var logged []ast.Expr
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			if id, ok := node.Fun.(*ast.Ident); !ok || id.Name != "make" || len(node.Args) < 2 {
				return true
			}
			switch node.Args[0].(type) {
			case *ast.MapType:
				// A size hint: the map holds what it is given either way.
				sizes[node.Args[1]] = true
			case *ast.ArrayType:
				// A slice's capacity. Its length is what it holds, and is still broken.
				if len(node.Args) == 3 {
					sizes[node.Args[2]] = true
				}
			case *ast.ChanType:
				// A buffer already asynchronous, grown by one. Zero to one is another channel -
				// a send that blocked returns - and is still made.
				if lit, ok := node.Args[1].(*ast.BasicLit); ok && lit.Value != "0" {
					sizes[lit] = true
				}
			}
		case *ast.BinaryExpr:
			// A duration's count: how long a timeout is, which no test holds without waiting it out.
			if node.Op == token.MUL {
				if timeUnit(node.Y) {
					sizes[node.X] = true
				}
				if timeUnit(node.X) {
					sizes[node.Y] = true
				}
			}
		case *ast.IfStmt:
			if node.Else == nil && onlyLogs(node.Body) {
				logged = append(logged, node.Cond)
			}
		}
		besideFailures(n, sizes)
		return true
	})
	return sizes, logged
}

// besideFailures marks the literals returned beside an error that is never nil: return 0, err
// under `if err != nil`, or beside a new error or a sentinel. Nobody reads them.
func besideFailures(n ast.Node, sizes map[ast.Node]bool) {
	switch node := n.(type) {
	case *ast.IfStmt:
		if checked := nonNil(node.Cond); checked != "" {
			for _, stmt := range node.Body.List {
				if ret, ok := stmt.(*ast.ReturnStmt); ok && failing(ret, checked) {
					unread(ret, sizes)
				}
			}
		}
	case *ast.ReturnStmt:
		if failing(node, "") {
			unread(node, sizes)
		}
	}
}

// nonNil is the name cond asks is not nil, as in `err != nil`; empty for any other condition.
func nonNil(cond ast.Expr) string {
	b, ok := cond.(*ast.BinaryExpr)
	if !ok || b.Op != token.NEQ {
		return ""
	}
	x, okX := b.X.(*ast.Ident)
	y, okY := b.Y.(*ast.Ident)
	if !okX || !okY || y.Name != "nil" {
		return ""
	}
	return x.Name
}

// failing says ret returns an error that is never nil: a new one (errors.New, fmt.Errorf,
// connect.NewError), a sentinel (ErrSomething), or checked, the name its if found not nil.
func failing(ret *ast.ReturnStmt, checked string) bool {
	if len(ret.Results) < 2 {
		return false
	}
	switch last := ret.Results[len(ret.Results)-1].(type) {
	case *ast.Ident:
		return last.Name == checked && checked != "" || sentinel.MatchString(last.Name)
	case *ast.SelectorExpr:
		return sentinel.MatchString(last.Sel.Name)
	case *ast.CallExpr:
		sel, ok := last.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		pkg, ok := sel.X.(*ast.Ident)
		return ok && (pkg.Name == "errors" && sel.Sel.Name == "New" ||
			pkg.Name == "fmt" && sel.Sel.Name == "Errorf" ||
			pkg.Name == "connect" && sel.Sel.Name == "NewError")
	}
	return false
}

// sentinel is a sentinel error's name, exported or not: ErrGone, errNoStore - never err itself,
// which may be nil.
var sentinel = regexp.MustCompile(`^[Ee]rr[A-Z]`)

// unread marks the literals ret returns beside its error as nothing a caller reads.
func unread(ret *ast.ReturnStmt, sizes map[ast.Node]bool) {
	for _, r := range ret.Results[:len(ret.Results)-1] {
		if lit, ok := r.(*ast.BasicLit); ok {
			sizes[lit] = true
		}
	}
}

func within(n ast.Node, spans []ast.Expr) bool {
	for _, s := range spans {
		if n.Pos() >= s.Pos() && n.End() <= s.End() {
			return true
		}
	}
	return false
}

func timeUnit(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "time" {
		return false
	}
	switch sel.Sel.Name {
	case "Nanosecond", "Microsecond", "Millisecond", "Second", "Minute", "Hour":
		return true
	}
	return false
}

// onlyLogs is a block of nothing but log lines, in this tree's idiom: log.G(ctx) or log.L, or
// a logger, then With* fields, then the level. Anything else in it - a return, an assignment -
// and the branch does something a test can see.
func onlyLogs(block *ast.BlockStmt) bool {
	if len(block.List) == 0 {
		return false
	}
	for _, stmt := range block.List {
		expr, ok := stmt.(*ast.ExprStmt)
		if !ok {
			return false
		}
		call, ok := expr.X.(*ast.CallExpr)
		if !ok || !logCall(call) {
			return false
		}
	}
	return true
}

func logCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "Trace", "Debug", "Info", "Warn", "Error":
	default:
		return false
	}
	// Down the chain to what it starts from: log (the package) or a logger.
	for x := sel.X; ; {
		switch v := x.(type) {
		case *ast.CallExpr:
			x = v.Fun
		case *ast.SelectorExpr:
			x = v.X
		case *ast.Ident:
			return v.Name == "log" || v.Name == "logger"
		default:
			return false
		}
	}
}

// edit is one mutation of one node, before it is given the function it is in.
type edit struct {
	node ast.Node
	what string
	with string
	site *site
	role role
}

// edits are the ways one node can be made to decide something else.
func edits(n ast.Node, source []byte, fset *token.FileSet) []edit {
	switch node := n.(type) {
	case *ast.BinaryExpr:
		if to, ok := swaps[node.Op]; ok {
			return []edit{{opPos{node.OpPos, node.Op}, fmt.Sprintf("%s becomes %s", node.Op, to), to.String(),
				binarySite(node, fset), roleSwap}}
		}
	case *ast.ReturnStmt:
		var out []edit
		for _, r := range node.Results {
			if b, ok := r.(*ast.BinaryExpr); ok && swaps[b.Op] != 0 {
				// A comparison's left side is not what it returns but for a bool compared with
				// another, and a schema cannot say which: that one is built alone.
				s := binarySite(b, fset)
				if s.kind == siteFlip {
					s = nil
				}
				out = append(out, edit{b, "the result is its left side alone", exprText(source, fset, b.X), s, roleLeft})
			}
		}
		return out
	case *ast.AssignStmt:
		// A field a function writes is the mark it leaves: p.last = now, and the tests that
		// never noticed it was gone.
		if len(node.Lhs) == 1 && len(node.Rhs) == 1 && node.Tok == token.ASSIGN {
			if _, ok := node.Lhs[0].(*ast.SelectorExpr); ok {
				return []edit{{node, "the field is not written", "_ = " + exprText(source, fset, node.Rhs[0]),
					pairSite(siteSet, node, node.Lhs[0], node.Rhs[0], fset), roleOnly}}
			}
		}
	case *ast.BasicLit:
		if node.Kind == token.INT {
			if v, err := strconv.Atoi(node.Value); err == nil {
				return []edit{{node, fmt.Sprintf("%d becomes %d", v, v+1), strconv.Itoa(v + 1),
					pairSite(siteLit, node, node, node, fset), roleOnly}}
			}
		}
	}
	return nil
}

// opPos is the operator's own position, as a node, so an edit replaces the operator alone
// rather than the expression around it.
type opPos struct {
	pos token.Pos
	op  token.Token
}

func (o opPos) Pos() token.Pos { return o.pos }
func (o opPos) End() token.Pos { return o.pos + token.Pos(len(o.op.String())) }

func exprText(source []byte, fset *token.FileSet, e ast.Expr) string {
	return string(source[fset.Position(e.Pos()).Offset:fset.Position(e.End()).Offset])
}

func touches(fset *token.FileSet, fn *ast.FuncDecl, changed map[int]bool) bool {
	// No lines is every line: the whole file, which is what StaleExempts plans.
	if changed == nil {
		return true
	}
	from, to := fset.Position(fn.Pos()).Line, fset.Position(fn.End()).Line
	for line := from; line <= to; line++ {
		if changed[line] {
			return true
		}
	}
	return false
}

// laneMarker, in a function's doc comment, says what holds the function is a lane this run
// cannot be: a test that needs root and KVM, as launching a machine does, followed by the name
// of that test. The whole function is left out, and the test must exist - among the files of
// any build tag - or the plan fails: a marker naming nothing is an exemption with no answer
// behind it. The test is in the function's package, or named with the package's directory from
// the module's root, internal/e2e.TestName: what wires a binary together is held by the lane
// that runs the binary, and nothing in its own package starts one.
const laneMarker = "mutate-lane:"

// heldByLane reports whether fn is marked as held by a lane's test, and checks that the test
// it names is there.
func heldByLane(path string, fn *ast.FuncDecl) (bool, error) {
	if fn.Doc == nil {
		return false, nil
	}
	_, rest, ok := strings.Cut(fn.Doc.Text(), laneMarker)
	if !ok {
		return false, nil
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return false, fmt.Errorf("%s: %s names no test after %q", path, fn.Name.Name, laneMarker)
	}
	test, dir := strings.TrimRight(fields[0], ".,;:"), filepath.Dir(path)
	if strings.Contains(test, "/") {
		// The test's name is the extension of the path's last element: e2e.TestName.
		name := pathpkg.Ext(test)
		root, err := moduleRoot(dir)
		if err != nil {
			return false, fmt.Errorf("%s: %s: %w", path, fn.Name.Name, err)
		}
		test, dir = strings.TrimPrefix(name, "."), filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(test, name)))
	}
	if !strings.HasPrefix(test, "Test") {
		return false, fmt.Errorf("%s: %s names no test after %q", path, fn.Name.Name, laneMarker)
	}
	tests, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		return false, err
	}
	for _, f := range tests {
		b, err := os.ReadFile(f)
		if err != nil {
			return false, err
		}
		if strings.Contains(string(b), "func "+test+"(") {
			return true, nil
		}
	}
	return false, fmt.Errorf("%s: %s is marked as held by %s, and no test in %s is called that", path, fn.Name.Name, test, dir)
}

// moduleRoot is the directory above dir that holds go.mod.
func moduleRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for d := abs; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d, nil
		}
		if filepath.Dir(d) == d {
			return "", fmt.Errorf("no go.mod above %s", dir)
		}
	}
}

// exemptLines are the lines a mutate-exempt comment is written on. The line under it is covered
// by its caller, which asks about the line it is on and the one above - so covering it here too
// would be a second answer to the same question, and one no test could tell from the first.
func exemptLines(fset *token.FileSet, file *ast.File) map[int]bool {
	out := map[int]bool{}
	for _, group := range file.Comments {
		if !isExempt(group) {
			continue
		}
		for _, c := range group.List {
			out[fset.Position(c.Pos()).Line] = true
		}
	}
	return out
}

// Overlay writes the file with this mutation in it under dir, never over the file itself, and
// gives back the overlay that has `go` build it in the file's place (go help build, -overlay).
// The tree is not touched, so mutations run side by side and the checkout stays usable while
// they do: editing in place, a run that was stopped left a mutation in the source.
func Overlay(m Mutation, dir string) (string, error) {
	source, err := os.ReadFile(m.File)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(m.File)
	if err != nil {
		return "", err
	}
	mutated := filepath.Join(dir, filepath.Base(m.File))
	if err := os.WriteFile(mutated, append(append(bytes.Clone(source[:m.Start]), m.With...), source[m.End:]...), 0o600); err != nil {
		return "", err
	}
	overlay, err := json.Marshal(map[string]map[string]string{"Replace": {abs: mutated}})
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "overlay.json")
	return path, os.WriteFile(path, overlay, 0o600)
}

// Outcome is what the tests did about one mutation.
type Outcome int

const (
	// Killed: a test refused the mutated code, which is the answer this gate wants.
	Killed Outcome = iota
	// Survived: every test passed with the behaviour changed.
	Survived
	// Unbuildable: the edit does not compile, so it asks nothing.
	Unbuildable
	// Untested: the package has no tests at all, so there was nobody to ask.
	Untested
)

// buildFailed is go test's word that a package it was to test did not build.
var buildFailed = regexp.MustCompile(`(?m)^FAIL\s+\S+\s+\[build failed\]$`)

// targetedTimeout is how long the tests that reach a mutated line may take.
const targetedTimeout = "1m"

// Asked is how one mutation is put to the tests.
type Asked struct {
	// Overlay is the file Overlay made, which has the mutation built in the file's place; empty
	// asks about the file as it is.
	Overlay string
	Timeout string
	// Reaching are the package's own tests that execute the mutated line (Coverage.Reaching),
	// and only those are run: a test that never reaches the line cannot refuse the edit. Nil is
	// not known, and every test is run; empty is none, and the package's tests are not run.
	Reaching []string
	// Importers are the packages asked when the package's own tests let the edit through.
	Importers []string
}

// Run builds and runs the tests of the package the mutated file belongs to, stopping at the
// first that fails.
//
// An edit its own package's tests let through is asked of the packages that import that one
// directly, when importers names any: a package's behaviour is often held by its caller's tests -
// the QMP client's file passing by the chain manager's - and counting those as survivors made the
// gate report tests that exist as missing. Only a survivor pays for the second run.
func Run(m Mutation, a Asked) (Outcome, string) {
	dir := filepath.Dir(m.File)
	// The pattern is a constant, so the only thing Glob has to say is what it matched.
	tests, _ := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if len(tests) == 0 && len(a.Importers) == 0 {
		return Untested, ""
	}
	// The package's own tests are run in its directory; its importers, named from the module's
	// root, from where this runs.
	type testRun struct {
		dir  string
		pkgs []string
		only []string
	}
	var runs []testRun
	if len(tests) > 0 && (a.Reaching == nil || len(a.Reaching) > 0) {
		runs = append(runs, testRun{dir: dir, pkgs: []string{"."}, only: a.Reaching})
	}
	if len(a.Importers) > 0 {
		runs = append(runs, testRun{pkgs: a.Importers})
	}
	var said strings.Builder
	for _, set := range runs {
		timeout := a.Timeout
		// mutate-exempt: which allowance a run gets; only a test an edit hangs tells them apart,
		// and only after a minute of it.
		if len(set.only) > 0 {
			// The few tests that reach the line, which ran in a fraction of this unmutated: an edit
			// that makes one loop for ever is refused as soon as this runs out, not after the
			// package's whole allowance.
			timeout = targetedTimeout
		}
		// -p 1: this run is one of the caller's budget of test processes, and go test over several
		// packages would otherwise run a binary per core, each with a PostgreSQL of its own.
		args := append(append([]string{"test"}, set.pkgs...), "-p", "1", "-count=1", "-failfast", "-timeout", timeout)
		if a.Overlay != "" {
			args = append(args, "-overlay", a.Overlay)
		}
		if len(set.only) > 0 {
			args = append(args, "-run", "^("+strings.Join(set.only, "|")+")$")
		}
		cmd := exec.Command("go", args...) //nolint:gosec // go test over this repository's packages
		cmd.Dir = set.dir
		out, err := cmd.CombinedOutput()
		said.Write(out)
		if err == nil {
			continue
		}
		// go test's own line, not the words anywhere: a test that fails may print them - this
		// package's do, about the modules they build - and that is an edit refused.
		if buildFailed.Match(out) || strings.Contains(string(out), "build constraints exclude") {
			return Unbuildable, said.String()
		}
		return Killed, said.String()
	}
	return Survived, said.String()
}

// Importers is, for each package directory this repository has, the directories of the packages
// whose code or tests import it directly: where Run looks for a test when the package's own let
// an edit through.
func Importers() (map[string][]string, error) {
	module, err := exec.Command("go", "list", "-m", "-f", "{{.Path}} {{.Dir}}").Output()
	if err != nil {
		return nil, fmt.Errorf("mutate: reading the module: %w", err)
	}
	path, root, ok := strings.Cut(strings.TrimSpace(string(module)), " ")
	if !ok {
		return nil, fmt.Errorf("mutate: reading the module: %q is not a path and a directory", module)
	}
	// From the module's root, wherever this runs from: `./...` anywhere else is part of it. -e
	// because a package that does not build still names its imports, and one that embeds a build
	// output (the control plane's dashboard) does not build in a checkout that has not built it.
	list := exec.Command("go", "list", "-e", "-f",
		`{{.ImportPath}} {{join .Imports " "}} {{join .TestImports " "}} {{join .XTestImports " "}}`, "./...")
	list.Dir = root
	var stderr strings.Builder
	list.Stderr = &stderr
	out, err := list.Output()
	prefix := path + "/"
	if err != nil {
		return nil, fmt.Errorf("mutate: listing the packages: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return importersFrom(prefix, string(out)), nil
}

// importersFrom is Importers over `go list` output: one line per package, its path then every
// path it or its tests import.
func importersFrom(prefix, listing string) map[string][]string {
	byDir := map[string][]string{}
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.HasPrefix(fields[0], prefix) {
			continue
		}
		importer := "./" + strings.TrimPrefix(fields[0], prefix)
		seen := map[string]bool{}
		for _, imported := range fields[1:] {
			dir, ok := strings.CutPrefix(imported, prefix)
			if !ok || seen[dir] || imported == fields[0] {
				continue
			}
			seen[dir] = true
			byDir[dir] = append(byDir[dir], importer)
		}
	}
	for dir := range byDir {
		sort.Strings(byDir[dir])
	}
	return byDir
}
