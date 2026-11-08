package gencache

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitIsGoGenerates(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string
		err  bool
	}{
		{line: "go tool mockgen  -package=x\tA", want: []string{"go", "tool", "mockgen", "-package=x", "A"}},
		{line: `echo "a b" c`, want: []string{"echo", "a b", "c"}},
		{line: `echo "a \"q\" b"`, want: []string{"echo", `a "q" b`}},
		{line: `echo "unterminated`, err: true},
		{line: `echo "a"b`, err: true},
		{line: `echo "\z"`, err: true},
		{line: `echo "" x`, want: []string{"echo", "", "x"}},
		{line: `echo "a\`, err: true},
		{line: "   ", want: nil},
	} {
		t.Run(tc.line, func(t *testing.T) {
			got, err := split(tc.line)
			if tc.err {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestDirectivesExpandAsGoGenerateDoes(t *testing.T) {
	dir := t.TempDir()
	body := "package p\n\n//go:generate echo $GOFILE $GOPACKAGE $GOLINE $DOLLAR $GOOS $GENCACHE_TEST_VAR  \n// go:generate not one\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "p.go"), []byte(body), 0o644))
	t.Setenv("GENCACHE_TEST_VAR", "fromenv")

	ds, err := directives(&pkg{Dir: dir}, "p.go", "p_test", map[string]string{"GOOS": "plan9"})
	require.NoError(t, err)
	require.Len(t, ds, 1)
	require.Equal(t, []string{"echo", "p.go", "p_test", "3", "$", "plan9", "fromenv"}, ds[0].words)
	require.Equal(t, "//go:generate echo $GOFILE $GOPACKAGE $GOLINE $DOLLAR $GOOS $GENCACHE_TEST_VAR", ds[0].text)
}

func TestAFileWithACommandAliasHasNoDirectiveKeyed(t *testing.T) {
	dir := t.TempDir()
	body := "package p\n\n//go:generate -command mg go tool mockgen\n//go:generate mg -destination=m.go -package=p . I\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "p.go"), []byte(body), 0o644))

	ds, err := directives(&pkg{Dir: dir}, "p.go", "p", nil)
	require.NoError(t, err)
	require.Len(t, ds, 1)
	require.Nil(t, ds[0].words)
	require.Equal(t, []string{"//go:generate -command mg go tool mockgen"}, ds[0].aliases)
	require.Equal(t, "a -command alias in its file", plan(ds[0]).reason)
}

func TestParseMockgenKeysOnlyWhatTheKeyCovers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		words  []string
		want   mockgen
		reason string
	}{
		{
			name:  "package mode",
			words: []string{"go", "tool", "mockgen", "-destination=m_test.go", "-package=p", "-mock_names=A=B", "example.com/q", "A"},
			want:  mockgen{tool: "mockgen", destination: "m_test.go", pkgPath: "example.com/q"},
		},
		{
			name:  "source mode, its arguments ignored as mockgen ignores them",
			words: []string{"go", "tool", "go.uber.org/mock/mockgen", "-source", "s.go", "-destination", "m_test.go", "A,B"},
			want:  mockgen{tool: "go.uber.org/mock/mockgen", destination: "m_test.go", source: "s.go"},
		},
		{name: "another generator", words: []string{"stringer", "-type=T"}, reason: "not go tool mockgen"},
		{name: "go alone", words: []string{"go"}, reason: "not go tool mockgen"},
		{name: "go tool alone", words: []string{"go", "tool"}, reason: "not go tool mockgen"},
		{name: "go run", words: []string{"go", "run", "x"}, reason: "not go tool mockgen"},
		{name: "another tool", words: []string{"go", "tool", "stringer", "-type=T"}, reason: "not go tool mockgen"},
		{name: "mockgen alone", words: []string{"go", "tool", "mockgen"}, reason: "no -destination"},
		{name: "mockgen from PATH", words: []string{"mockgen", "-destination=m.go", "q", "A"}, reason: "not go tool mockgen"},
		{name: "an unknown flag", words: []string{"go", "tool", "mockgen", "-destination=m.go", "-nope", "q", "A"}, reason: "a flag mockgen v0.6 does not have"},
		{name: "aux files", words: []string{"go", "tool", "mockgen", "-destination=m.go", "-source=s.go", "-aux_files=a=b.go"}, reason: "-aux_files"},
		{name: "build flags", words: []string{"go", "tool", "mockgen", "-destination=m.go", "-build_flags=-tags=x", "q", "A"}, reason: "-build_flags"},
		{name: "copyright file", words: []string{"go", "tool", "mockgen", "-destination=m.go", "-copyright_file=c.txt", "q", "A"}, reason: "-copyright_file"},
		{name: "archive mode", words: []string{"go", "tool", "mockgen", "-destination=m.go", "-archive=a.a", "q", "A"}, reason: "-archive"},
		{name: "a gob model", words: []string{"go", "tool", "mockgen", "-destination=m.go", "-model_gob=m.gob"}, reason: "-model_gob"},
		{name: "the version", words: []string{"go", "tool", "mockgen", "-version"}, reason: "writes no mock"},
		{name: "stdout", words: []string{"go", "tool", "mockgen", "q", "A"}, reason: "no -destination"},
		{name: "one argument", words: []string{"go", "tool", "mockgen", "-destination=m.go", "q"}, reason: "arguments mockgen would refuse"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := parseMockgen(tc.words)
			require.Equal(t, tc.reason, reason)
			if tc.reason == "" {
				require.Equal(t, tc.want, got)
			}
		})
	}
}

func TestPlanOfASourceThatDoesNotParse(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "s.go"), []byte("not go"), 0o644))
	d := directive{pkg: &pkg{ImportPath: "example.com/m/a", Dir: dir}, words: []string{"go", "tool", "mockgen", "-source=s.go", "-destination=x_test.go"}}
	require.Equal(t, "its -source does not parse", plan(d).reason)
}

func TestPlanReadsTheImportsOfASource(t *testing.T) {
	dir := t.TempDir()
	body := "package a\n\nimport (\n\t\"C\"\n\t\"example.com/q\"\n)\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "s.go"), []byte(body), 0o644))
	d := directive{pkg: &pkg{ImportPath: "example.com/m/a", Dir: dir}, words: []string{"go", "tool", "mockgen", "-source=s.go", "-destination=x_test.go"}}
	s := plan(d)
	require.Empty(t, s.reason)
	require.Equal(t, []string{"example.com/q"}, s.roots, "cgo's C is no package")
}

func TestPlanResolvesARelativePackageAgainstTheDirectives(t *testing.T) {
	d := directive{pkg: &pkg{ImportPath: "example.com/m/a", Dir: "/m/a"}, words: []string{"go", "tool", "mockgen", "-destination=x_test.go", "./sub", "I"}}
	s := plan(d)
	require.Empty(t, s.reason)
	require.Equal(t, []string{"example.com/m/a/sub"}, s.roots)
	require.Equal(t, "/m/a/x_test.go", s.dest)
}
