package linkweight

import (
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
		"main": "",
	} {
		if got := receiverType(name); got != want {
			t.Errorf("receiverType(%q) = %q, want %q", name, got, want)
		}
	}
}
