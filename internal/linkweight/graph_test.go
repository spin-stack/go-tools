package linkweight

import (
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func parse(t *testing.T, dump string) *Graph {
	t.Helper()
	g, err := ParseDeps(strings.NewReader(dump))
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// The shape -dumpdep prints: "_" the root, markers after either side, a line with no source for a
// method the method pass kept, and what go build itself says among the edges.
//
//nolint:dupword // a linker graph names a symbol on both sides of its edges
const dump = `# example.com/m
_ -> _rt0_amd64_linux
_rt0_amd64_linux -> runtime.main
runtime.main -> main.main
main.main -> example.com/m/lib.Render
example.com/m/lib.Render -> text/template.(*Template).Execute
text/template.(*Template).Execute -> text/template.(*state).evalField <ReflectMethod>
text/template.(*state).evalField <ReflectMethod> -> reflect.Value.MethodByName
main.main -> type:*example.com/m/lib.Conn <UsedInIface>
 -> example.com/m/lib.(*Conn).Export <ReflectMethod>
type:example.com/m/lib.Value <UsedInIface> -> example.com/m/lib.Value.Method <ReflectMethod>
main.main -> type:example.com/m/lib.Value
example.com/m/lib.(*Conn).Export -> go:string."<nil>"
`

func TestParseDepsReadsEdgesMarkersAndTheMethodPass(t *testing.T) {
	g := parse(t, dump)
	for _, s := range []string{"_", "main.main", "reflect.Value.MethodByName", "example.com/m/lib.(*Conn).Export", `go:string."<nil>"`} {
		if !g.Has(s) {
			t.Errorf("%s is linked and the graph does not have it", s)
		}
	}
	// A marker is not part of the name, and a string that looks like one is not a marker.
	if g.Has("text/template.(*state).evalField <ReflectMethod>") || g.Has("") {
		t.Error("a marker, or the method pass's empty source, was read as a symbol")
	}
	got := g.ReflectMethod()
	slices.Sort(got)
	want := []string{"example.com/m/lib.(*Conn).Export", "example.com/m/lib.Value.Method", "text/template.(*state).evalField"}
	if !slices.Equal(got, want) {
		t.Errorf("ReflectMethod = %v, want %v", got, want)
	}
}

func TestParseDepsRefusesAnEmptyGraph(t *testing.T) {
	if _, err := ParseDeps(strings.NewReader("# example.com/m\nok\n")); err == nil {
		t.Fatal("a build without -dumpdep read as a binary that links nothing")
	}
}

func TestChainIsAShortestPathFromTheRoot(t *testing.T) {
	g := parse(t, dump)
	c, via := g.Chain("text/template.(*state).evalField")
	want := []string{"_", "_rt0_amd64_linux", "runtime.main", "main.main", "example.com/m/lib.Render",
		"text/template.(*Template).Execute", "text/template.(*state).evalField"}
	if !slices.Equal(c, want) || via {
		t.Errorf("Chain = %v via a method %v, want %v by calls", c, via, want)
	}
	// A type's own chain ends at the type: no method is entered.
	if c, via := g.Chain("type:example.com/m/lib.Value"); via || !slices.Equal(c, []string{"_", "_rt0_amd64_linux", "runtime.main", "main.main", "type:example.com/m/lib.Value"}) {
		t.Errorf("the type's chain = %v via a method %v", c, via)
	}
	if c, _ := g.Chain("nothing.Links"); c != nil {
		t.Errorf("a symbol the binary does not link has the chain %v", c)
	}
}

func TestChainEntersAKeptMethodThroughItsType(t *testing.T) {
	g := parse(t, dump)
	// No edge into it: the method pass kept it, and its receiver's type is the way in.
	c, via := g.Chain("example.com/m/lib.(*Conn).Export")
	if !via || c[len(c)-2] != "type:*example.com/m/lib.Conn" || c[0] != "_" {
		t.Errorf("Chain = %v via a method %v, want it through type:*example.com/m/lib.Conn", c, via)
	}
	// A type -> method edge the linker printed is the same thing said aloud.
	c, via = g.Chain("example.com/m/lib.Value.Method")
	if !via || c[len(c)-2] != "type:example.com/m/lib.Value" {
		t.Errorf("Chain = %v via a method %v, want it through type:example.com/m/lib.Value", c, via)
	}
}

func TestChainOfACycleWithNoWayInIsTheSymbol(t *testing.T) {
	g := parse(t, "a.F -> a.G\na.G -> a.F\n") //nolint:dupword // a linker graph names a symbol on both sides of its edges
	if c, via := g.Chain("a.F"); !slices.Equal(c, []string{"a.F"}) || via {
		t.Errorf("Chain = %v, %v", c, via)
	}
}

func TestReceiverType(t *testing.T) {
	for name, want := range map[string]string{
		"github.com/godbus/dbus/v5.(*Conn).Export": "type:*github.com/godbus/dbus/v5.Conn",
		"encoding/json/v2.addressableValue.Method": "type:encoding/json/v2.addressableValue",
		"a.M":  "type:a",
		"main": "",
	} {
		if got := receiverType(name); got != want {
			t.Errorf("receiverType(%q) = %q, want %q", name, got, want)
		}
	}
}

// A program built as linkweight builds one: what ships is the loaded sections, which the program
// headers say independently, and no symbol is empty or memory the file does not hold.
func TestReadLayoutCountsWhatTheFileShips(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() { println(1) }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "prog")
	build := exec.Command("go", "build", "-o", bin, "main.go")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOOS=linux", "CGO_ENABLED=0", "GOFLAGS=")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	l, err := ReadLayout(bin)
	if err != nil {
		t.Fatal(err)
	}
	f, err := elf.Open(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var loaded uint64
	for _, p := range f.Progs {
		if p.Type == elf.PT_LOAD {
			loaded += p.Filesz
		}
	}
	if l.Shipped > loaded || l.Shipped < loaded*9/10 {
		t.Errorf("shipped %d, the loaded segments %d", l.Shipped, loaded)
	}
	if l.PCLNTab == 0 || l.PCLNTab >= l.Shipped {
		t.Errorf("pclntab %d of %d", l.PCLNTab, l.Shipped)
	}
	byName := map[string]Sym{}
	for _, s := range l.Symbols {
		if s.Size == 0 {
			t.Errorf("%s is empty and listed", s.Name)
		}
		byName[s.Name] = s
	}
	// runtime.mheap_ is the heap's state, in .bss: memory at run time, no byte of the file.
	if _, ok := byName["runtime.mheap_"]; ok {
		t.Error("a .bss symbol is counted as shipped")
	}
	if !byName["runtime.main"].Code || byName["runtime.buildVersion"].Code || byName["runtime.buildVersion"].Size == 0 {
		t.Errorf("runtime.main %+v, runtime.buildVersion %+v", byName["runtime.main"], byName["runtime.buildVersion"])
	}
	if _, err := ReadLayout(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Error("a file that is not there read as a binary")
	}
}
