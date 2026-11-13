package gitexec_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/internal/gitexec"
)

const (
	runsGit = "package p\n\nimport \"os/exec\"\n\nfunc run() error { return exec.Command(\"git\", \"config\", \"user.name\", \"Ana\").Run() }\n"
	fenced  = "package p\n\nimport (\n\t\"testing\"\n\n\t\"github.com/spin-stack/go-tools/internal/gittest\"\n)\n\nfunc TestMain(m *testing.M) { gittest.Fence(); m.Run() }\n"
	plain   = "package p\n\nimport \"testing\"\n\nfunc TestRun(t *testing.T) { _ = run() }\n"
)

// This repository's own tests: every git one runs goes through internal/gittest, or runs in a
// package whose TestMain fences it. A finding here is the test to change, not a line to allow.
func TestNoTestHereRunsGitUnfenced(t *testing.T) {
	found, err := gitexec.Check(filepath.Join("..", ".."))
	require.NoError(t, err)
	for _, f := range found {
		t.Error(f)
	}
}

// Which trees hold a git a test can run unfenced: each case is a module of its own, and the
// findings are named by the test file or the package that has to change.
func TestAGitATestCanRunUnfencedIsFound(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{
			name:  "a test's own exec of git",
			files: map[string]string{"p/a_test.go": "package p\n\nimport (\n\t\"os/exec\"\n\t\"testing\"\n)\n\nfunc TestA(t *testing.T) { _ = exec.CommandContext(t.Context(), \"git\", \"init\").Run() }\n"},
			want:  []string{"p/a_test.go"},
		},
		{
			name:  "under another name for os/exec",
			files: map[string]string{"p/a_test.go": "package p\n\nimport (\n\tx \"os/exec\"\n\t\"testing\"\n)\n\nfunc TestA(t *testing.T) { _ = x.Command(\"git\", \"init\").Run() }\n"},
			want:  []string{"p/a_test.go"},
		},
		{
			name: "each, in the order of the tree",
			files: map[string]string{
				"b/x_test.go": "package b\n\nimport \"os/exec\"\n\nvar _ = exec.Command(\"git\")\n",
				"a/x_test.go": "package a\n\nimport \"os/exec\"\n\nvar _ = exec.Command(\"git\")\n",
			},
			want: []string{"a/x_test.go", "b/x_test.go"},
		},
		{
			name:  "a lookup of git, which runs nothing",
			files: map[string]string{"p/a_test.go": "package p\n\nimport \"os/exec\"\n\nvar _, _ = exec.LookPath(\"git\")\n"},
		},
		{
			name:  "a program named by a variable, which this cannot read",
			files: map[string]string{"p/a_test.go": "package p\n\nimport \"os/exec\"\n\nfunc run(name string) { _ = exec.Command(name).Run() }\n"},
		},
		{
			name:  "a test's exec of another program",
			files: map[string]string{"p/a_test.go": "package p\n\nimport (\n\t\"os/exec\"\n\t\"testing\"\n)\n\nfunc TestA(t *testing.T) { _ = exec.Command(\"ssh-keygen\", \"git\").Run() }\n"},
		},
		{
			name:  "a call of another package's Command",
			files: map[string]string{"p/a_test.go": "package p\n\nimport (\n\t\"os/exec\"\n\t\"testing\"\n)\n\nvar _ = exec.ErrNotFound\n\nfunc TestA(t *testing.T) { _ = other.Command(\"git\") }\n"},
		},
		{
			name:  "a package's git its tests run unfenced",
			files: map[string]string{"p/run.go": runsGit, "p/run_test.go": plain},
			want:  []string{"p"},
		},
		{
			name:  "a package's git its TestMain fences",
			files: map[string]string{"p/run.go": runsGit, "p/run_test.go": plain, "p/main_test.go": fenced},
		},
		{
			name: "a package's git its TestMain fences with another package's Fence",
			files: map[string]string{
				"p/run.go":       runsGit,
				"p/run_test.go":  plain,
				"p/main_test.go": "package p\n\nimport \"testing\"\n\nfunc TestMain(m *testing.M) { other.Fence(); m.Run() }\n",
			},
			want: []string{"p"},
		},
		{
			name:  "a package's git no test runs",
			files: map[string]string{"p/run.go": runsGit},
		},
		{
			name: "what go does not build: testdata, a worktree, a _ directory, gittest itself",
			files: map[string]string{
				"p/testdata/a_test.go":             "package p\n\nimport \"os/exec\"\n\nvar _ = exec.Command(\"git\")\n",
				".wt/x/p/a_test.go":                "package p\n\nimport \"os/exec\"\n\nvar _ = exec.Command(\"git\")\n",
				"_output/p/a_test.go":              "package p\n\nimport \"os/exec\"\n\nvar _ = exec.Command(\"git\")\n",
				"node_modules/p/a_test.go":         "package p\n\nimport \"os/exec\"\n\nvar _ = exec.Command(\"git\")\n",
				"internal/gittest/gittest.go":      runsGit,
				"internal/gittest/gittest_test.go": plain,
				"notes.txt":                        "exec.Command(\"git\")\n",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for name, body := range tc.files {
				require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.Dir(name)), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(body), 0o600))
			}
			found, err := gitexec.Check(root)
			require.NoError(t, err)
			var keys []string
			for _, f := range found {
				keys = append(keys, f.Key)
				assert.NotEmpty(t, f.Why)
				assert.Positive(t, f.Line)
			}
			assert.Equal(t, tc.want, keys)
		})
	}
}

// A finding reads as a line to go to, and a file go cannot parse is an error, not a pass.
func TestAFindingSaysWhereAndAnUnparsableFileFails(t *testing.T) {
	assert.Equal(t, "p/a_test.go:7: why", gitexec.Finding{File: "p/a_test.go", Line: 7, Why: "why"}.String())

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "broken.go"), []byte("package"), 0o600))
	_, err := gitexec.Check(root)
	require.Error(t, err)
}
