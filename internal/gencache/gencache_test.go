package gencache

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fixture is a module with three kinds of directive: mockgen in package mode, mockgen in
// source mode, and a generator that is not mockgen. Its go.mod and go.sum are this module's, so
// mockgen and what it needs come from the module cache this module's build filled, and no test
// reaches the network.
var fixture = map[string]string{
	"iface/iface.go": `package iface

type Store interface {
	Get(key string) (string, error)
}
`,
	"other/other.go": `package other

func Other() {}
`,
	"use/use.go": `package use

import "example.com/m/iface"

func Use(s iface.Store) (string, error) { return s.Get("k") }
`,
	"use/use_test.go": `package use

//go:generate go tool mockgen -destination=mock_store_test.go -package=use example.com/m/iface Store
//go:generate go tool mockgen -destination=mock_named_test.go -package=use -mock_names=Store=NamedStore example.com/m/iface Store
`,
	"src/src.go": `package src

import "example.com/m/iface"

//go:generate go tool mockgen -source=src.go -destination=mock_src_test.go -package=src

type Clock interface {
	Now() int64
	Store() iface.Store
}
`,
	"self/self.go": `package self

//go:generate go tool mockgen -destination=mock_self_test.go -package=self . Doer

type Doer interface {
	Do() error
}
`,
	"tagged/tagged.go": `//go:build e2e

package tagged

//go:generate go tool mockgen -source=tagged.go -destination=mock_tagged_test.go -package=tagged

type Waiter interface {
	Wait()
}
`,
	"tagged/doc.go": `package tagged
`,
	"stamp/stamp.go": `package stamp

//go:generate go run ../cmd/stamp
`,
	"cmd/stamp/main.go": `package main

import (
	"fmt"
	"os"
)

// Appends a line each time it runs, so a test sees how many times it ran.
func main() {
	f, err := os.OpenFile("stamps.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if _, err := f.WriteString("ran\n"); err != nil {
		panic(err)
	}
	fmt.Println("stamped")
}
`,
}

// keyed is how many of the fixture's directives, without tags, gencache keys.
const keyed = 4

// mockgenFetched puts mockgen's modules in the module cache, through this module's own go.mod
// and whatever proxy the machine uses: building this module's tests fetches none of a tool's,
// and the fixtures run with the proxy off. A warm cache makes it a no-op.
var mockgenFetched = sync.OnceValues(func() ([]byte, error) {
	cmd := exec.Command("go", "tool", "-n", "mockgen")
	cmd.Dir = filepath.Join("..", "..")
	return cmd.CombinedOutput()
})

func newModule(t *testing.T, extra map[string]string) string {
	t.Helper()
	out, err := mockgenFetched()
	require.NoError(t, err, string(out))
	dir := t.TempDir()
	mod, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	require.NoError(t, err)
	sum, err := os.ReadFile(filepath.Join("..", "..", "go.sum"))
	require.NoError(t, err)
	mod = []byte("module example.com/m" + string(mod[bytes.IndexByte(mod, '\n'):]))
	files := map[string]string{"go.mod": string(mod), "go.sum": string(sum)}
	for _, set := range []map[string]string{fixture, extra} {
		for name, body := range set {
			files[name] = body
		}
	}
	writeTree(t, dir, files)
	return dir
}

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
}

// generate runs gencache from a directory that is not the module's, so a go command it runs in
// the wrong directory finds no module.
func generate(t *testing.T, dir, cache, tags string) (Result, string, error) {
	t.Helper()
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOWORK", "off")
	t.Chdir(t.TempDir())
	var out bytes.Buffer
	res, err := Run(t.Context(), Options{Dir: dir, Tags: tags, Cache: cache, Jobs: 4, Out: &out})
	return res, out.String(), err
}

func mustGenerate(t *testing.T, dir, cache, tags string) Result {
	t.Helper()
	res, out, err := generate(t, dir, cache, tags)
	require.NoError(t, err, out)
	return res
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	require.NoError(t, err)
	return string(b)
}

func edit(t *testing.T, dir, name, old, new string) {
	t.Helper()
	body := read(t, dir, name)
	require.Contains(t, body, old)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(strings.Replace(body, old, new, 1)), 0o644))
}

func TestFirstRunRunsEveryDirectiveAndTheSecondRunsOnlyWhatIsNotKeyed(t *testing.T) {
	dir, cache := newModule(t, nil), t.TempDir()
	notKeyed := map[string]int{"not go tool mockgen": 1}

	first, out, err := generate(t, dir, cache, "")
	require.NoError(t, err, out)
	require.Equal(t, Result{Directives: keyed + 1, Ran: keyed, NotKeyed: notKeyed}, first)
	require.Contains(t, out, "run: "+filepath.Join(dir, "use", "use_test.go")+":4: go tool mockgen -destination=mock_named_test.go")
	require.Contains(t, out, "\nstamped\n", "what a generator prints is printed")
	mock := read(t, dir, "use/mock_store_test.go")
	require.Contains(t, mock, "func (m *MockStore) Get(")
	require.Contains(t, read(t, dir, "use/mock_named_test.go"), "func (m *NamedStore) Get(", "a package's second directive ran")

	second, out, err := generate(t, dir, cache, "")
	require.NoError(t, err, out)
	require.Equal(t, Result{Directives: keyed + 1, FromCache: keyed, NotKeyed: notKeyed}, second)
	require.NotContains(t, out, "run: ")
	require.Equal(t, mock, read(t, dir, "use/mock_store_test.go"))
	require.Equal(t, 2, strings.Count(read(t, dir, "stamp/stamps.txt"), "ran\n"), "the directive that is not mockgen runs every time")
}

func TestAChangeRunsTheMocksThatReadItAndOnlyThose(t *testing.T) {
	for _, tc := range []struct {
		name          string
		file, old     string
		new           string
		ran           int
		mock, mockHas string
	}{
		{
			name: "the mocked interface", file: "iface/iface.go", old: "Get(key string) (string, error)",
			new: "Get(key string) (string, error)\n\tPut(key, value string) error", ran: 3,
			mock: "use/mock_store_test.go", mockHas: "func (m *MockStore) Put(",
		},
		{
			name: "a source-mode file", file: "src/src.go", old: "Now() int64",
			new: "Now() int64\n\tSince(t int64) int64", ran: 1,
			mock: "src/mock_src_test.go", mockHas: "func (m *MockClock) Since(",
		},
		{name: "a package no mock reads", file: "other/other.go", old: "func Other() {}", new: "func Other() { _ = 1 }"},
		{name: "a package beside the mock", file: "use/use.go", old: `s.Get("k")`, new: `s.Get("key")`},
		{name: "the module's go line", file: "go.mod", old: "\ngo 1.27.0\n", new: "\ngo 1.27.1\n", ran: keyed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, cache := newModule(t, nil), t.TempDir()
			mustGenerate(t, dir, cache, "")
			edit(t, dir, tc.file, tc.old, tc.new)

			res := mustGenerate(t, dir, cache, "")
			require.Equal(t, tc.ran, res.Ran)
			require.Equal(t, keyed-tc.ran, res.FromCache)
			if tc.mock != "" {
				require.Contains(t, read(t, dir, tc.mock), tc.mockHas)
			}
		})
	}
}

func TestWhatNoMockReadsIsNotInTheKey(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
	}{
		{"a file that is not Go", map[string]string{"iface/notes.txt": "notes"}},
		// self is read by package mode alone, which type-checks no test; src reads iface in
		// source mode, which parses its tests.
		{"a test of a package only package mode reads", map[string]string{"self/self_test.go": "package self\n"}},
		{"a directory inside the package's", map[string]string{"iface/inner/inner.go": "package inner\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, cache := newModule(t, nil), t.TempDir()
			mustGenerate(t, dir, cache, "")
			writeTree(t, dir, tc.files)

			res := mustGenerate(t, dir, cache, "")
			require.Equal(t, 0, res.Ran)
			require.Equal(t, keyed, res.FromCache)
		})
	}
}

func TestTheBuildConfigurationIsInTheKey(t *testing.T) {
	dir, cache := newModule(t, nil), t.TempDir()
	mustGenerate(t, dir, cache, "")

	// A flag that changes neither what mockgen reads nor its build: only the key sees it.
	t.Setenv("GOFLAGS", "-mod=mod -modcacherw")
	var out bytes.Buffer
	res, err := Run(t.Context(), Options{Dir: dir, Cache: cache, Jobs: 1, Out: &out})
	require.NoError(t, err, out.String())
	require.Equal(t, keyed, res.Ran)
}

func TestKeysDoNotDependOnWhereTheCheckoutIs(t *testing.T) {
	dir, cache := newModule(t, nil), t.TempDir()
	mustGenerate(t, dir, cache, "")
	elsewhere := filepath.Join(t.TempDir(), "deeper", "checkout")
	require.NoError(t, os.CopyFS(elsewhere, os.DirFS(dir)))

	res := mustGenerate(t, elsewhere, cache, "")
	require.Equal(t, 0, res.Ran)
	require.Equal(t, keyed, res.FromCache)
}

func TestAMockThatIsNotWhatTheCacheHasIsWrittenBack(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spoil func(path string) error
	}{
		{"deleted", os.Remove},
		{"edited by hand", func(path string) error { return os.WriteFile(path, []byte("package use\n"), 0o644) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, cache := newModule(t, nil), t.TempDir()
			mustGenerate(t, dir, cache, "")
			path := filepath.Join(dir, "use", "mock_store_test.go")
			want := read(t, dir, "use/mock_store_test.go")
			require.NoError(t, tc.spoil(path))

			res, out, err := generate(t, dir, cache, "")
			require.NoError(t, err, out)
			require.Equal(t, 0, res.Ran)
			require.Equal(t, 1, res.Rewritten)
			require.Contains(t, out, "from the cache: "+path)
			require.Equal(t, want, read(t, dir, "use/mock_store_test.go"))
		})
	}
}

// Source mode reads every file of the -source file's directory, and the mock is one of them.
func TestAMockBesideWhatItMocksIsNotPartOfItsOwnKey(t *testing.T) {
	dir, cache := newModule(t, nil), t.TempDir()
	mustGenerate(t, dir, cache, "")
	require.Contains(t, read(t, dir, "src/mock_src_test.go"), "type MockClock struct")
	require.Contains(t, read(t, dir, "self/mock_self_test.go"), "type MockDoer struct")

	res := mustGenerate(t, dir, cache, "")
	require.Equal(t, 0, res.Ran, "the mock it wrote made what it read another thing")
}

func TestTagsChooseTheFilesWhoseDirectivesRun(t *testing.T) {
	dir, cache := newModule(t, nil), t.TempDir()

	require.Equal(t, keyed+1, mustGenerate(t, dir, cache, "").Directives)
	require.NoFileExists(t, filepath.Join(dir, "tagged", "mock_tagged_test.go"))

	res := mustGenerate(t, dir, cache, "e2e")
	require.Equal(t, keyed+2, res.Directives)
	require.FileExists(t, filepath.Join(dir, "tagged", "mock_tagged_test.go"))
}

// copied is a package whose second directive, a generator gencache cannot key, writes the
// interface its third mocks from spec.txt, after its first has read the package.
var copied = map[string]string{
	"copied/copied.go": `package copied

//go:generate go tool mockgen -destination=mock_source_test.go -package=copied . Source
//go:generate go run ../cmd/copyiface
//go:generate go tool mockgen -destination=mock_copied_test.go -package=copied . Copied

type Source interface {
	A()
}
`,
	"copied/spec.txt": "B",
	// Committed, as generated code is.
	"copied/gen.go": "package copied\n\ntype Copied interface {\n\tB()\n}\n",
	"cmd/copyiface/main.go": `package main

import "os"

func main() {
	spec, err := os.ReadFile("spec.txt")
	if err != nil {
		panic(err)
	}
	body := "package copied\n\ntype Copied interface {\n\t" + string(spec) + "()\n}\n"
	if err := os.WriteFile("gen.go", []byte(body), 0o644); err != nil {
		panic(err)
	}
}
`,
}

func TestAMockReadAfterAGeneratorItCannotKeyReadsWhatTheGeneratorWrote(t *testing.T) {
	dir, cache := newModule(t, copied), t.TempDir()
	mustGenerate(t, dir, cache, "")
	require.Contains(t, read(t, dir, "copied/mock_copied_test.go"), "func (m *MockCopied) B(")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "copied", "spec.txt"), []byte("C"), 0o644))
	mustGenerate(t, dir, cache, "")
	require.Contains(t, read(t, dir, "copied/mock_copied_test.go"), "func (m *MockCopied) C(")
}

// A directive gencache cannot key goes to go generate, which says what is wrong with it.
func TestADirectiveItCannotKeyFailsAsGoGenerateFails(t *testing.T) {
	for _, tc := range []struct {
		name   string
		files  map[string]string
		reason string
		says   string
	}{
		{
			name:   "an unterminated quote",
			files:  map[string]string{"bad/bad.go": "package bad\n\n//go:generate go tool mockgen \"-destination=m_test.go\n"},
			reason: "go generate could not read it", says: "mismatched quoted string",
		},
		{
			name:   "a tool go.mod does not have",
			files:  map[string]string{"bad/bad.go": "package bad\n\n//go:generate go tool example.com/none/mockgen -destination=m_test.go example.com/m/iface Store\n"},
			reason: "go tool -n example.com/none/mockgen failed", says: "example.com/none/mockgen",
		},
		{
			name:   "a package that is not there",
			files:  map[string]string{"bad/bad.go": "package bad\n\n//go:generate go tool mockgen -destination=m_test.go -package=bad example.com/m/nothere Store\n"},
			reason: "go list cannot load a package it reads", says: "nothere",
		},
		{
			name: "a package whose import is not there",
			files: map[string]string{
				"broken/broken.go": "package broken\n\nimport _ \"example.com/m/nothere\"\n\ntype I interface{ M() }\n",
				"bad/bad.go":       "package bad\n\n//go:generate go tool mockgen -destination=m_test.go -package=bad example.com/m/broken I\n",
			},
			reason: "go list cannot load a package it reads", says: "nothere",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, cache := newModule(t, tc.files), t.TempDir()

			res, out, err := generate(t, dir, cache, "")
			require.Error(t, err)
			require.Contains(t, err.Error(), filepath.Join(dir, "bad", "bad.go")+":3:")
			require.Equal(t, 1, res.NotKeyed[tc.reason], res.NotKeyed)
			require.Contains(t, out, tc.says)
			require.Equal(t, keyed, res.Ran, "the other packages went on")
		})
	}
}

func TestAGoListThatFailsSaysWhy(t *testing.T) {
	dir := newModule(t, nil)
	t.Chdir(t.TempDir())
	_, err := Run(t.Context(), Options{Dir: dir, Patterns: []string{"-nosuchflag"}, Cache: t.TempDir(), Jobs: 1, Out: &bytes.Buffer{}})
	require.ErrorContains(t, err, "flag provided but not defined: -nosuchflag")
}

func TestTrimRemovesEntriesUnusedForAWeek(t *testing.T) {
	c := cache{dir: t.TempDir()}
	old, recent := strings.Repeat("a", 64), strings.Repeat("b", 64)
	require.NoError(t, c.put(old, []byte("old")))
	require.NoError(t, c.put(recent, []byte("recent")))
	stale := time.Now().Add(-unused - time.Hour)
	require.NoError(t, os.Chtimes(c.path(old), stale, stale))

	require.NoError(t, c.trim())
	_, ok := c.get(old)
	require.False(t, ok)
	b, ok := c.get(recent)
	require.True(t, ok)
	require.Equal(t, "recent", string(b))
}

func TestAHitMarksTheEntryUsed(t *testing.T) {
	c := cache{dir: t.TempDir()}
	key := strings.Repeat("c", 64)
	require.NoError(t, c.put(key, []byte("x")))
	stale := time.Now().Add(-unused + time.Hour)
	require.NoError(t, os.Chtimes(c.path(key), stale, stale))

	_, ok := c.get(key)
	require.True(t, ok)
	fi, err := os.Stat(c.path(key))
	require.NoError(t, err)
	require.WithinDuration(t, time.Now(), fi.ModTime(), time.Minute)
}

func TestTrimOfACacheNotYetMade(t *testing.T) {
	require.NoError(t, cache{dir: filepath.Join(t.TempDir(), "absent")}.trim())
}
