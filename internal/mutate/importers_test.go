package mutate

import (
	"reflect"
	"testing"
)

// Who imports a package is read off `go list`: its code and its tests, this module's packages
// only, and never the package itself - a package's own tests are asked first, not as its caller.
func TestAPackagesImportersAreTheOnesThatNameItInThisModule(t *testing.T) {
	const prefix = "example.com/spin/"
	// As `go list -f '{{.ImportPath}} {{.Imports}} {{.TestImports}} {{.XTestImports}}'` writes
	// it: a package whose external tests import it lists itself, and is not its own importer.
	listing := `example.com/spin/internal/qmp context encoding/json  example.com/spin/internal/qmp testing
example.com/spin/internal/qcow example.com/spin/internal/qmp fmt example.com/spin/internal/qmp
example.com/spin/internal/agent fmt  example.com/spin/internal/qmp
example.com/spin/internal/vm example.com/spin/internal/qmp
github.com/other/lib example.com/spin/internal/qmp
example.com/spin/internal/solo strings  example.com/spin/internal/solo
`
	got := importersFrom(prefix, listing)
	want := map[string][]string{
		// qcow names it twice, once in its code and once in its tests: one importer.
		"internal/qmp": {"./internal/agent", "./internal/qcow", "./internal/vm"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("importers are %v, want %v", got, want)
	}
}
