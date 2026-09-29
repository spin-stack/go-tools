package ctxlife_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/internal/ctxlife"
)

// Each case is a function body; the gate reports it or it does not. The first is the shape that
// took every workspace's DNS: a server started under the context of the operation that made it,
// with the loop in another function, so nothing in the goroutine itself looks like a loop.
func TestAGoroutineIsHeldToTheContextItIsGiven(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		reported   bool
	}{
		{"a server under its starter's context", `
func serve(ctx context.Context, s server) {
	go func() { _ = s.Serve(ctx) }()
}`, true},
		{"a goroutine given the context by call", `
func serve(ctx context.Context, s server) {
	go s.Serve(ctx)
}`, true},
		{"one under a context derived from the caller's", `
func serve(ctx context.Context, s server) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	_ = cancel
	go s.Serve(ctx)
}`, true},
		{"one detached first", `
func serve(ctx context.Context, s server) {
	ctx = context.WithoutCancel(ctx)
	go s.Serve(ctx)
}`, false},
		{"one given a detached context in the call", `
func serve(ctx context.Context, s server) {
	go s.Serve(context.WithoutCancel(ctx))
}`, false},
		{"one detached only after it was started", `
func serve(ctx context.Context, s server) {
	go s.Serve(ctx)
	ctx = context.WithoutCancel(ctx)
}`, true},
		{"one the function waits for", `
func serve(ctx context.Context, s server) error {
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	return <-done
}`, false},
		{"one the function waits for with a group", `
func serve(ctx context.Context, s server) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = s.Serve(ctx) }()
	wg.Wait()
}`, false},
		{"one that says why it ends with the call", `
func serve(ctx context.Context, s server) {
	// ctx-lifetime: the request's, which it answers
	go s.Serve(ctx)
}`, false},
		{"one that does not use the context", `
func serve(ctx context.Context, s server) {
	go s.Serve(context.Background())
}`, false},
		{"a function literal with a context of its own", `
func serve(ctx context.Context, s server) {
	go func(ctx context.Context) { _ = s.Serve(ctx) }(context.Background())
}`, false},
		{"a goroutine in a literal, under the literal's parameter", `
func start(s server) func(context.Context) {
	return func(ctx context.Context) { go s.Serve(ctx) }
}`, true},
		// What counts as detaching is the context package's own, on the parameter itself.
		{"another variable detached, and the parameter used", `
func serve(ctx context.Context, s server) {
	other := context.WithoutCancel(ctx)
	_ = other
	go s.Serve(ctx)
}`, true},
		{"a field given the detached context, and the parameter used", `
func serve(ctx context.Context, s server) {
	s.ctx = context.WithoutCancel(ctx)
	go s.Serve(ctx)
}`, true},
		{"a WithoutCancel that is not the context package's", `
func serve(ctx context.Context, s server) {
	go s.Serve(xcontext.WithoutCancel(ctx))
}`, true},
		{"the parameter given a value from a call that is not detaching", `
func serve(ctx context.Context, s server) {
	ctx = s.Context()
	go s.Serve(ctx)
}`, true},
		// Only a parameter written context.Context is a context.
		{"a parameter of another type", `
func serve(ctx Context, d context.CancelFunc, s server) {
	go s.Serve(ctx, d)
}`, false},
		{"a Context of another package", `
func serve(ctx xcontext.Context, s server) {
	go s.Serve(ctx)
}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "f.go")
			require.NoError(t, os.WriteFile(path, []byte("package p\n"+tc.body+"\n"), 0o600))
			found, err := ctxlife.CheckFile(path)
			require.NoError(t, err)
			if tc.reported {
				assert.Len(t, found, 1, "not reported")
			} else {
				assert.Empty(t, found, "reported")
			}
		})
	}
}

// The tree is read the way the build reads it: every package's Go files, and not its tests, its
// test fixtures, what is vendored, or what a build or a cache wrote (_output, a dot-directory).
func TestTheWholeTreeIsReadAndOnlyItsCode(t *testing.T) {
	const flagged = "package p\nfunc serve(ctx context.Context, s server) { go s.Serve(ctx) }\n"
	root := t.TempDir()
	for path, body := range map[string]string{
		"a.go":                 flagged,
		"deep/down/b.go":       flagged,
		"a_test.go":            flagged,
		"testdata/c.go":        flagged,
		"vendor/d.go":          flagged,
		"_output/e.go":         flagged,
		".cache/mkosi/f.go":    flagged,
		"notes.txt":            flagged,
		"deep/clean.go":        "package p\n",
		"deep/down/testdata.x": flagged,
	} {
		full := filepath.Join(root, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o700))
		require.NoError(t, os.WriteFile(full, []byte(body), 0o600))
	}

	found, err := ctxlife.Check(root)
	require.NoError(t, err)
	var files []string
	for _, f := range found {
		rel, err := filepath.Rel(root, f.File)
		require.NoError(t, err)
		files = append(files, rel)
	}
	assert.ElementsMatch(t, []string{"a.go", filepath.Join("deep", "down", "b.go")}, files)

	_, err = ctxlife.Check(filepath.Join(root, "missing"))
	assert.Error(t, err, "a tree that is not there was read as one with nothing in it")
	require.NoError(t, os.WriteFile(filepath.Join(root, "broken.go"), []byte("package"), 0o600))
	_, err = ctxlife.Check(root)
	assert.Error(t, err, "a file that does not parse was read as one with nothing in it")
}
