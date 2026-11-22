package consumers_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spin-stack/go-tools/consumers"
)

func write(t *testing.T, table string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), consumers.File)
	if err := os.WriteFile(path, []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestATableIsItsRepositoryAndItsConsumers(t *testing.T) {
	got, err := consumers.Load(write(t, `name = "brand"
scopes = ["@fontsource", "@fontsource-variable"]

[[consumer]]
name = "website"
repo = "spin-stack/website"
pin = "versions.yaml"
packages = "package.json"
[consumer.copies]
"src/styles/brand-tokens.css" = "dist/tokens.css"

[[consumer]]
name = "grafana"
repo = "spin-stack/spin"
pin = "versions.yaml"
`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "brand" || !slices.Equal(got.Scopes, []string{"@fontsource", "@fontsource-variable"}) || len(got.Consumers) != 2 {
		t.Fatalf("read %+v", got)
	}
	site := got.Consumers[0]
	if site.Name != "website" || site.Repo != "spin-stack/website" || site.Pin != "versions.yaml" ||
		site.Packages != "package.json" || site.Copies["src/styles/brand-tokens.css"] != "dist/tokens.css" {
		t.Errorf("website is %+v", site)
	}
	if grafana := got.Consumers[1]; grafana.Packages != "" || len(grafana.Copies) != 0 {
		t.Errorf("grafana is %+v", grafana)
	}
}

func TestATableThatCannotBeHeldIsRefused(t *testing.T) {
	const row = "[[consumer]]\nname = \"a\"\nrepo = \"o/a\"\npin = \"versions.yaml\"\n"
	for name, tc := range map[string]struct{ table, says string }{
		"a misspelt field":       {"name = \"brand\"\n" + row + "package = \"package.json\"\n", "consumer.package is no field"},
		"a repository unnamed":   {row, "no name"},
		"a consumer unnamed":     {"name = \"brand\"\n[[consumer]]\nrepo = \"o/a\"\npin = \"v\"\n", `a consumer's name is its own: ""`},
		"one name twice":         {"name = \"brand\"\n" + row + row, `a consumer's name is its own: "a"`},
		"a repo with a URL":      {"name = \"brand\"\n[[consumer]]\nname = \"a\"\nrepo = \"https://github.com/o/a\"\npin = \"v\"\n", "is not owner/name"},
		"no place for its pin":   {"name = \"brand\"\n[[consumer]]\nname = \"a\"\nrepo = \"o/a\"\n", "a names no versions.yaml"},
		"a scope with its slash": {"name = \"brand\"\nscopes = [\"@fontsource\", \"@fontsource/\"]\n" + row, `scope "@fontsource/" is not an npm scope`},
		"a scope with no @":      {"name = \"brand\"\nscopes = [\"fontsource\"]\n" + row, `scope "fontsource" is not an npm scope`},
		"a scope and more":       {"name = \"brand\"\nscopes = [\"x@fontsource\"]\n" + row, `scope "x@fontsource" is not an npm scope`},
		"one scope, not a list":  {"name = \"brand\"\nscope = \"@fontsource\"\n" + row, "scope is no field"},
		"what is not TOML":       {"name = brand", "expected value"},
		"a file that is not one": {"", ""},
	} {
		t.Run(name, func(t *testing.T) {
			path := write(t, tc.table)
			if tc.table == "" {
				path = filepath.Join(t.TempDir(), "absent.toml")
			}
			got, err := consumers.Load(path)
			if err == nil || !strings.Contains(err.Error(), tc.says) || got.Name != "" || got.Consumers != nil {
				t.Fatalf("read %+v, %v; want a refusal saying %q", got, err, tc.says)
			}
		})
	}
}
