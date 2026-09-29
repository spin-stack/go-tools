package mutate

import (
	"os"
	"path/filepath"
	"testing"
)

// An edit in a package whose coverage could not be measured is asked of every test, not of none:
// no coverage is not knowing which tests reach the line, and a test that reaches nothing is only
// what a measurement says.
func TestAnEditWithNoCoverageIsAskedOfEveryTest(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":       "module example.com/nocover\n\ngo 1.24\n",
		"code.go":      "package nocover\n\nfunc Double(n int) int {\n\treturn n + n\n}\n",
		"code_test.go": "package nocover\n\nimport \"testing\"\n\nfunc TestDouble(t *testing.T) {\n\tif Double(2) != 4 {\n\t\tt.Fatal(\"not doubled\")\n\t}\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := Plan(filepath.Join(dir, "code.go"), map[int]bool{4: true})
	if err != nil || len(plan) == 0 {
		t.Fatalf("plan: %v, %v", plan, err)
	}
	for i := range plan {
		plan[i].ID = i + 1
	}
	s, err := Schemata(dir, plan, t.TempDir())
	if err != nil || s.Binary == "" || len(s.Built) == 0 {
		t.Fatalf("schema: %+v, %v", s, err)
	}
	p := pkgRun{dir: dir, timeout: "2m", slots: make(chan struct{}, 1)}
	for _, m := range s.Built {
		if got := p.built(m, s, nil); got != Killed {
			t.Errorf("%s: %v with no coverage, want refused by the test nobody measured", m, got)
		}
	}
}
