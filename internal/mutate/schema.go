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
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// A package's mutations are built into one test binary, each behind a switch its ID turns on
// (SPIN_MUTATE_ID), rather than each into a test binary of its own. A build is most of what an
// edit costs - the package, everything in the test binary that imports it, and the link of a
// binary that carries the AWS SDK and pgx - and one per edit, a few at once, was what made this
// gate take a machine down. Running a built binary again is cheap. This is what Stryker and mull
// call mutant schemata.
//
// Each edit is written so its operands are evaluated once, as the code evaluates them, and no
// line moves, so a line's coverage is still the line's: a comparison is XORed with its switch,
// since every swap of one is its negation; && and || keep the right side behind a func, which is
// their short circuit; the rest go through a generic helper the schema adds to the package. An
// edit a schema cannot say without types - a literal that must be an int64, a string + - fails
// to build, and the compiler says where: those edits are built alone, as before.

// switchEnv is the variable a schema's switches read: the ID of the one mutation switched on.
const switchEnv = "SPIN_MUTATE_ID"

// site is one node a schema rewrites, and what of it each of its edits needs.
type site struct {
	kind siteKind
	// start and end are the node's offsets in the file; x and y its operands' - the two sides of
	// a binary expression, a field and its value, or the literal twice.
	start, end, xs, xe, ys, ye int
	// from and to are the lines the node spans.
	from, to int
	op       token.Token
}

type siteKind int

const (
	siteFlip  siteKind = iota + 1 // a comparison
	siteLogic                     // && or ||
	siteArith                     // + or -
	siteSet                       // a field written
	siteLit                       // an integer literal
)

// role is which of a site's edits a mutation is: a binary expression is one site with two, its
// operator swapped and its result its left side alone.
type role int

const (
	roleSwap role = iota + 1
	roleLeft
	roleOnly
)

func binarySite(b *ast.BinaryExpr, fset *token.FileSet) *site {
	s := pairSite(siteFlip, b, b.X, b.Y, fset)
	s.op = b.Op
	switch b.Op {
	case token.LAND, token.LOR:
		s.kind = siteLogic
	case token.ADD, token.SUB:
		s.kind = siteArith
	}
	return s
}

func pairSite(kind siteKind, n, x, y ast.Node, fset *token.FileSet) *site {
	off := func(p token.Pos) int { return fset.Position(p).Offset }
	return &site{kind: kind, start: off(n.Pos()), end: off(n.End()), xs: off(x.Pos()), xe: off(x.End()),
		ys: off(y.Pos()), ye: off(y.End()), from: fset.Position(n.Pos()).Line, to: fset.Position(n.End()).Line}
}

// anchor is one site with the IDs of its edits in the schema: zero where the edit is not in it.
type anchor struct {
	s                *site
	swap, left, only int
}

// render is one file's source with every mutation of ms in it, each behind its switch.
func render(source []byte, ms []Mutation) string {
	byNode := map[[3]int]*anchor{}
	for _, m := range ms {
		key := [3]int{m.site.start, m.site.end, int(m.site.kind)}
		a := byNode[key]
		if a == nil {
			a = &anchor{s: m.site}
			byNode[key] = a
		}
		switch m.role {
		case roleSwap:
			a.swap = m.ID
		case roleLeft:
			a.left = m.ID
		case roleOnly:
			a.only = m.ID
		}
	}
	anchors := make([]*anchor, 0, len(byNode))
	for _, a := range byNode {
		anchors = append(anchors, a)
	}
	// Outermost first: of two nodes that start together, the longer holds the other.
	sort.Slice(anchors, func(i, j int) bool {
		if anchors[i].s.start != anchors[j].s.start {
			return anchors[i].s.start < anchors[j].s.start
		}
		return anchors[i].s.end > anchors[j].s.end
	})
	r := renderer{source: source, anchors: anchors}
	return r.text(0, len(source))
}

type renderer struct {
	source  []byte
	anchors []*anchor
}

// text is source[from:to] with each outermost site in it rewritten, and the sites inside that one
// rewritten within it.
func (r renderer) text(from, to int) string {
	var b strings.Builder
	at := from
	for _, a := range r.anchors {
		if a.s.start < at || a.s.end > to {
			continue
		}
		b.Write(r.source[at:a.s.start])
		b.WriteString(r.schema(a))
		at = a.s.end
	}
	b.Write(r.source[at:to])
	return b.String()
}

func (r renderer) schema(a *anchor) string {
	s := a.s
	if s.kind == siteLit {
		return fmt.Sprintf("mutateSchemaInt(%d, %s)", a.only, r.source[s.start:s.end])
	}
	x, y := r.text(s.xs, s.xe), r.text(s.ys, s.ye)
	between := r.source[s.xe:s.ys]
	// What lay between the operands - the operator and the space around it - goes; its line
	// breaks stay, where a break is allowed: after a comma.
	breaks := strings.Repeat("\n", bytes.Count(between, []byte("\n")))
	switch s.kind {
	case siteFlip:
		return fmt.Sprintf("((%s%s%s) != mutateSchemaOn(%d))", x, between, y, a.swap)
	case siteLogic:
		return fmt.Sprintf("mutateSchemaLogic(%d, %d, %t, %s,%s func() bool { return %s })",
			a.swap, a.left, s.op == token.LAND, x, breaks, y)
	case siteArith:
		return fmt.Sprintf("mutateSchemaArith(%d, %d, %t, %s,%s %s)", a.swap, a.left, s.op == token.ADD, x, breaks, y)
	default: // siteSet
		return fmt.Sprintf("mutateSchemaSet(%d, &%s,%s %s)", a.only, x, breaks, y)
	}
}

// helperImports and helper are what a schema adds to one file of the package: the switches and the
// helpers the edits call. The imports go on the package clause's line and the helpers after the
// last, so no line of the file moves.
const helperImports = `; import mutateSchemaOS "os"; import mutateSchemaStrconv "strconv"`

const helper = `

var mutateSchemaActive, _ = mutateSchemaStrconv.Atoi(mutateSchemaOS.Getenv(%q))

func mutateSchemaOn(id int) bool { return id != 0 && id == mutateSchemaActive }

type mutateSchemaNumber interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr |
		~float32 | ~float64 | ~complex64 | ~complex128
}

func mutateSchemaLogic(swap, left int, and bool, x bool, y func() bool) bool {
	if mutateSchemaOn(left) {
		return x
	}
	if and != mutateSchemaOn(swap) {
		return x && y()
	}
	return x || y()
}

func mutateSchemaArith[T mutateSchemaNumber](swap, left int, add bool, x, y T) T {
	if mutateSchemaOn(left) {
		return x
	}
	if add != mutateSchemaOn(swap) {
		return x + y
	}
	return x - y
}

func mutateSchemaSet[T any](id int, field *T, v T) {
	if !mutateSchemaOn(id) {
		*field = v
	}
}

func mutateSchemaInt[T mutateSchemaNumber](id int, v T) T {
	if mutateSchemaOn(id) {
		return v + 1
	}
	return v
}
`

// Schema is one package's mutations built into one binary.
type Schema struct {
	// Overlay has go build the package with every mutation of Built in it.
	Overlay string
	// Binary is the package's test binary; empty where it has no tests. It is built without
	// coverage: go's -cover builds the files on disk, not the overlay's. Its lines are the files'
	// own, so the coverage of the package as it is (Cover) says which tests reach each edit.
	Binary string
	// Built are the mutations in it, each switched on by its ID.
	Built []Mutation
	// Alone are those a schema could not build: each is built on its own, through Overlay.
	Alone []Mutation
}

// Schemata builds every mutation of ms, which are all in the package in dir, into one test binary
// under work. A mutation the compiler refuses there is moved to Alone and the rest built again;
// errors it cannot put on a mutation's line move them all.
func Schemata(dir string, ms []Mutation, work string) (*Schema, error) {
	if err := os.MkdirAll(work, 0o700); err != nil {
		return nil, err
	}
	s := &Schema{}
	for _, m := range ms {
		if m.site == nil {
			s.Alone = append(s.Alone, m)
		} else {
			s.Built = append(s.Built, m)
		}
	}
	tests := hasTests(dir)
	for len(s.Built) > 0 {
		overlay, err := writeSchema(dir, s.Built, work)
		if err != nil {
			return nil, err
		}
		bin := ""
		build := exec.Command("go", "build", "-gcflags=-e", "-overlay", overlay, "-o", os.DevNull, ".")
		if tests {
			bin = filepath.Join(work, "pkg.test")
			build = exec.Command("go", "test", "-c", "-vet=off", "-gcflags=-e", "-overlay", overlay, "-o", bin, ".")
		}
		build.Dir = dir
		out, err := combined("schema: build the package's tests", build)
		if err == nil {
			s.Overlay, s.Binary = overlay, bin
			return s, nil
		}
		refused := refusedBy(string(out), s.Built)
		if len(refused) == 0 {
			s.Alone = append(s.Alone, s.Built...)
			s.Built = nil
			break
		}
		var kept []Mutation
		for _, m := range s.Built {
			if refused[m.site] {
				s.Alone = append(s.Alone, m)
			} else {
				kept = append(kept, m)
			}
		}
		s.Built = kept
	}
	return s, nil
}

// compileError is the file and line of an error go build reports: path/name.go:12:5: ...
var compileError = regexp.MustCompile(`(?m)([^\s:]+\.go):(\d+):\d+: `)

// refusedBy is the sites the compiler's errors in out are on: the one that starts on the error's
// line, or the innermost that spans it. Empty where an error is on none, which no edit explains.
func refusedBy(out string, built []Mutation) map[*site]bool {
	refused := map[*site]bool{}
	// mutate-exempt: any n below zero is every match.
	for _, e := range compileError.FindAllStringSubmatch(out, -1) {
		line, _ := strconv.Atoi(e[2]) // the pattern's digits
		// A site that starts on the line over one that only spans it, then the smaller.
		better := func(s, than *site) bool {
			if (s.from == line) != (than.from == line) {
				return s.from == line
			}
			return s.end-s.start < than.end-than.start
		}
		var on *site
		for _, m := range built {
			s := m.site
			if filepath.Base(m.File) != filepath.Base(e[1]) || line < s.from || line > s.to {
				continue
			}
			if on == nil || better(s, on) {
				on = s
			}
		}
		if on == nil {
			return nil
		}
		refused[on] = true
	}
	return refused
}

// writeSchema writes each file of ms with its mutations in it, and the helpers, under work, and the
// overlay that puts them in the package's place.
func writeSchema(dir string, ms []Mutation, work string) (string, error) {
	byFile := map[string][]Mutation{}
	for _, m := range ms {
		byFile[m.File] = append(byFile[m.File], m)
	}
	files := make([]string, 0, len(byFile))
	for file := range byFile {
		files = append(files, file)
	}
	sort.Strings(files)
	replace := map[string]string{}
	for i, file := range files {
		source, err := os.ReadFile(file) //nolint:gosec // a file of this repository's diff
		if err != nil {
			return "", err
		}
		text := render(source, byFile[file])
		if i == 0 {
			// The package clause is before every site, so its offset is the same in the text.
			f, err := parser.ParseFile(token.NewFileSet(), file, source, parser.PackageClauseOnly)
			if err != nil {
				return "", err
			}
			at := f.Name.End() - f.FileStart
			text = text[:at] + helperImports + text[at:] + fmt.Sprintf(helper, switchEnv)
		}
		abs, err := filepath.Abs(file)
		if err != nil {
			return "", err
		}
		written := filepath.Join(work, filepath.Base(file))
		if err := os.WriteFile(written, []byte(text), 0o600); err != nil {
			return "", err
		}
		replace[abs] = written
	}
	overlay, err := json.Marshal(map[string]map[string]string{"Replace": replace})
	if err != nil {
		return "", err
	}
	path := filepath.Join(work, "overlay.json")
	return path, os.WriteFile(path, overlay, 0o600)
}

// Ask runs a test binary in dir with mutation id switched on, stopping at the first test that
// fails: only, when it names any, and every test when it is nil.
func Ask(bin, dir string, id int, timeout string, only []string) (Outcome, string) {
	// mutate-exempt: which allowance a run gets; only a test an edit hangs tells them apart, and
	// only after a minute of it.
	if len(only) > 0 {
		timeout = targetedTimeout
	}
	args := []string{"-test.count=1", "-test.failfast", "-test.v", "-test.timeout", timeout}
	if len(only) > 0 {
		args = append(args, "-test.run", "^("+strings.Join(only, "|")+")$")
	}
	cmd := exec.Command(bin, args...) //nolint:gosec // a test binary this run built
	cmd.Dir = dir
	cmd.Env = testEnv(switchEnv + "=" + strconv.Itoa(id))
	out, exceeded, err := bounded("schema: run one edit", cmd)
	countTests(dir, out)
	if exceeded {
		return Exceeded, string(out)
	}
	if err != nil {
		return Killed, string(out)
	}
	return Survived, string(out)
}

// ImporterBinary builds the tests of importer, a package named from the module's root, at out,
// with the schema overlay puts in place of the package it imports, and says the directory they
// run in. Empty where it has no tests.
func ImporterBinary(importer, overlay, out string) (string, string, error) {
	root, err := moduleRoot(".")
	// mutate-exempt: there is no go.mod above only outside a module, where no importer is named.
	if err != nil {
		return "", "", err
	}
	build := exec.Command("go", "test", "-c", "-vet=off", "-overlay", overlay, "-o", out, importer)
	build.Dir = root
	if said, err := combined("schema: build an importer's tests", build); err != nil {
		return "", "", fmt.Errorf("mutate: building %s's tests with the schema: %w: %s", importer, err, said)
	}
	if _, err := os.Stat(out); err != nil {
		return "", "", nil //nolint:nilerr // no test binary: the importer has no tests
	}
	return out, filepath.Join(root, importer), nil
}
