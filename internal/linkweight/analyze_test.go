package linkweight

import (
	"slices"
	"strings"
	"testing"
)

func TestPackageOf(t *testing.T) {
	pkgs := map[string]bool{
		"gopkg.in/yaml.v3": true, "gopkg.in/yaml": true, "example.com/m/lib": true,
		"text/template": true, "io": true, "github.com/a/b": true,
	}
	for sym, want := range map[string]string{
		"gopkg.in/yaml.v3.Marshal":                     "gopkg.in/yaml.v3",
		"gopkg.in/yaml.v3.(*decoder).unmarshal":        "gopkg.in/yaml.v3",
		"example.com/m/lib.Render.func1":               "example.com/m/lib",
		"example.com/m/lib.Map[go.shape.string,io.R]":  "example.com/m/lib",
		"type:*example.com/m/lib.Conn":                 "example.com/m/lib",
		"type:.eq.example.com/m/lib.Conn":              "example.com/m/lib",
		"go:itab.*example.com/m/lib.Conn,io.Writer":    "example.com/m/lib",
		"text/template.(*state).evalField":             "text/template",
		"main.main":                                    "example.com/m/cmd/a",
		"main..inittask":                               "example.com/m/cmd/a",
		`go:string."hello"`:                            Unattributed,
		"gclocals·AkCBhdbIlfnjk7LjaGwO4g==":            Unattributed,
		"github.com/a/bee.F":                           Unattributed,
		"runtime.pclntab":                              Unattributed,
		"github.com/a/b.(*T[github.com/c/d.X]).Method": "github.com/a/b",
	} {
		if got := PackageOf(sym, pkgs, "example.com/m/cmd/a"); got != want {
			t.Errorf("PackageOf(%q) = %q, want %q", sym, got, want)
		}
	}
}

// pkg is a package of the analysed module or of a dependency.
func pkg(path, module string, imports ...string) Package {
	p := Package{ImportPath: path, Imports: imports}
	if module != Std {
		p.Module = &struct {
			Path string `json:"Path"`
		}{module}
	}
	return p
}

// fixture is a binary of example.com/m: main imports lib and cloud; cloud is the only importer of
// the sdk; lib uses text/template; the sdk starts a goroutine in its init that execs.
func fixture(t *testing.T) Input {
	t.Helper()
	const m = "example.com/m"
	//nolint:dupword // a linker graph names a symbol on both sides of its edges
	dump := `_ -> _rt0_amd64_linux
_rt0_amd64_linux -> runtime.main
runtime.main -> main.main
main.main -> example.com/m/lib.Render
main.main -> example.com/m/cloud.New
example.com/m/cloud.New -> example.com/sdk/svc.(*Client).Get
example.com/m/lib.Render -> text/template.(*state).evalField <ReflectMethod>
text/template.(*state).evalField <ReflectMethod> -> reflect.Value.Call
_ -> go:main.inittasks
go:main.inittasks -> example.com/sdk/svc..inittask
example.com/sdk/svc..inittask -> example.com/sdk/svc.init.0
example.com/sdk/svc.init.0 -> os.StartProcess
main.main -> net.(*sysListener).listenUnix
main.main -> net.(*sysListener).listenTCP
`
	return Input{
		Root: "cmd/a", MainPath: m + "/cmd/a", Module: m,
		Packages: []Package{
			pkg(m+"/cmd/a", m, m+"/lib", m+"/cloud", "net", "runtime"),
			pkg(m+"/lib", m, "text/template"),
			pkg(m+"/cloud", m, "example.com/sdk/svc"),
			pkg("example.com/sdk/svc", "example.com/sdk", "os"),
			pkg("text/template", Std, "reflect"),
			pkg("reflect", Std, "runtime"), pkg("os", Std, "runtime"), pkg("net", Std, "runtime"), pkg("runtime", Std),
		},
		Layout: Layout{
			Shipped: 4 << 20,
			PCLNTab: 1 << 20,
			Symbols: []Sym{
				{"main.main", 64 << 10, true},
				{"example.com/m/lib.Render", 64 << 10, true},
				{"example.com/sdk/svc.(*Client).Get", 1 << 20, true},
				{"type:example.com/sdk/svc.Client", 1 << 20, false},
				{"text/template.(*state).evalField", 192 << 10, true},
				{"runtime.main", 704 << 10, true},
				{`go:string."x"`, 100, false},
			},
		},
		Graph: parse(t, dump),
	}
}

func TestAnalyzeWeighsEachModuleWithItsShareOfThePCLNTab(t *testing.T) {
	b := Analyze(fixture(t))
	if b.Shipped != 4<<20 || b.Unattributed != 100 {
		t.Errorf("shipped %d, unattributed %d", b.Shipped, b.Unattributed)
	}
	// The sdk's 1 MiB of code is half the code, so half the pclntab is its.
	want := Weight{"example.com/sdk", 2<<20 + 512<<10}
	if b.Modules[0] != want {
		t.Errorf("heaviest module = %+v, want %+v", b.Modules[0], want)
	}
	var total uint64
	for _, m := range b.Modules {
		total += m.Bytes
	}
	if total+b.Unattributed != 3<<20+1<<20+100 {
		t.Errorf("the modules and the unattributed add up to %d, not every symbol and the pclntab", total+b.Unattributed)
	}
}

func TestAnalyzeNamesWhatTurnsMethodEliminationOff(t *testing.T) {
	b := Analyze(fixture(t))
	if b.MethodElimination || len(b.Breakers) != 1 {
		t.Fatalf("method elimination %v, breakers %+v", b.MethodElimination, b.Breakers)
	}
	f := b.Breakers[0]
	want := []string{"cmd/a", "lib", "text/template"}
	if f.Name != "text/template" || f.Kind != KindReflectMethod || !slices.Equal(f.Chain, want) || f.ViaMethod {
		t.Errorf("breaker = %+v, want text/template by %v", f, want)
	}
}

func TestBreakersInReflectAloneAreNamedAsReflect(t *testing.T) {
	g := parse(t, "_ -> main.main\nmain.main -> reflect.Value.Method <ReflectMethod>\nmain.main -> type:x.T <ReflectMethod>\n") //nolint:dupword // a linker graph names a symbol on both sides of its edges
	on, found := breakers(g, func(s string) string { return strings.SplitN(s, ".", 2)[0] }, func(string) ([]string, bool) { return nil, false })
	if on || len(found) != 1 || found[0].Name != "reflect" || found[0].Symbol != "reflect.Value.Method" {
		t.Errorf("on %v, found %+v", on, found)
	}
	on, found = breakers(parse(t, "_ -> main.main\n"), nil, nil)
	if !on || found != nil {
		t.Errorf("a graph with no mark: on %v, found %+v", on, found)
	}
}

func TestAnalyzeFindsCapabilitiesAndWhoBringsThemIn(t *testing.T) {
	in := fixture(t)
	in.Layout.Symbols = append(in.Layout.Symbols, Sym{"x_cgo_init", 8, true})
	b := Analyze(in)
	got := map[string]Finding{}
	for _, f := range b.Capabilities {
		got[f.Name] = f
	}
	if len(got) != 4 || got["cgo"].Symbol != "x_cgo_init" {
		t.Fatalf("capabilities = %+v, want cgo, exec, listen, reflect-call", b.Capabilities)
	}
	// Reached from a package's init: the imports that bring the package in are the chain's head.
	if want := []string{"cmd/a", "cloud", "example.com/sdk/svc", "os"}; !slices.Equal(got["exec"].Chain, want) {
		t.Errorf("exec's chain = %v, want %v", got["exec"].Chain, want)
	}
	// A prefix answers the smallest name it matches, whatever order the linker printed.
	if got["listen"].Symbol != "net.(*sysListener).listenTCP" {
		t.Errorf("listen = %s", got["listen"].Symbol)
	}
}

func TestCutsAreTheImportsOfTheModuleWorthCutting(t *testing.T) {
	b := Analyze(fixture(t))
	if len(b.Cuts) != 4 {
		t.Fatalf("cuts = %+v", b.Cuts)
	}
	// cmd/a > cloud and cloud > sdk both unlink the sdk; the tie goes by name.
	first := Cut{From: "cloud", To: "example.com/sdk/svc", Bytes: 2<<20 + 512<<10, Modules: []string{"example.com/sdk"}}
	if c := b.Cuts[0]; c.From != first.From || c.To != first.To || c.Bytes != first.Bytes || !slices.Equal(c.Modules, first.Modules) {
		t.Errorf("first cut = %+v, want %+v", c, first)
	}
	if c := b.Cuts[2]; c.From != "cmd/a" || c.To != "lib" || c.Bytes != 384<<10 || len(c.Modules) != 0 {
		t.Errorf("the template cut = %+v: std is still linked, so no module goes", c)
	}
	// An import of the standard library by a dependency is no cut of the module's; one under
	// MinCut is not reported.
	for _, c := range b.Cuts {
		if c.From == "example.com/sdk/svc" || c.Bytes < MinCut {
			t.Errorf("cut %+v", c)
		}
	}
}

func TestCutsOfAMainPackageNotListedAreNone(t *testing.T) {
	if c := cuts("example.com/m/cmd/none", "example.com/m", fixture(t).Packages, nil); c != nil {
		t.Errorf("cuts = %+v", c)
	}
}

func TestParsePackagesReadsGoListsStream(t *testing.T) {
	ps, err := ParsePackages(strings.NewReader(`{"ImportPath":"os","Standard":true}
{"ImportPath":"example.com/m/lib","Imports":["os"],"Module":{"Path":"example.com/m"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 || ps[0].module() != Std || ps[1].module() != "example.com/m" || ps[1].Imports[0] != "os" {
		t.Errorf("packages = %+v", ps)
	}
	if _, err := ParsePackages(strings.NewReader(`{"ImportPath":`)); err == nil {
		t.Error("a broken stream read as packages")
	}
}

// A breaker is named by the smallest of its package's marked functions, whatever order the linker
// printed them in: the same build says the same thing.
//
//nolint:dupword // a linker graph names a symbol on both sides of its edges
func TestABreakerIsItsPackagesSmallestFunction(t *testing.T) {
	g := parse(t, `_ -> main.main
main.main -> b.Z <ReflectMethod>
main.main -> b.A <ReflectMethod>
main.main -> b.M <ReflectMethod>
main.main -> reflect.Z <ReflectMethod>
main.main -> reflect.A <ReflectMethod>
main.main -> reflect.M <ReflectMethod>
`)
	pkgOf := func(s string) string { return strings.SplitN(s, ".", 2)[0] }
	none := func(string) ([]string, bool) { return nil, false }
	if _, found := breakers(g, pkgOf, none); len(found) != 1 || found[0].Symbol != "b.A" {
		t.Errorf("found %+v, want b by b.A", found)
	}
	g = parse(t, "_ -> main.main\nmain.main -> reflect.Z <ReflectMethod>\nmain.main -> reflect.A <ReflectMethod>\nmain.main -> reflect.M <ReflectMethod>\n")
	if _, found := breakers(g, pkgOf, none); len(found) != 1 || found[0].Symbol != "reflect.A" {
		t.Errorf("found %+v, want reflect by reflect.A", found)
	}
}

// A module is linked when one of its packages puts a byte in the binary, and not when go list
// names its packages and the linker keeps none of their code.
func TestAModuleCountsFromItsFirstByte(t *testing.T) {
	pkgs := []Package{pkg("m/a", "m", "x/one", "y/none"), pkg("x/one", "x"), pkg("y/none", "y")}
	g, ok := newImportGraph("m/a", pkgs)
	if !ok {
		t.Fatal("the main package is listed")
	}
	got := g.modules(g.reach(g.start, g.start), map[string]uint64{"m/a": 10, "x/one": 1})
	if !got["m"] || !got["x"] || got["y"] || len(got) != 2 {
		t.Errorf("modules = %v, want m and x", got)
	}
}
