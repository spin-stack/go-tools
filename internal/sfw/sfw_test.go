package sfw_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/internal/sfw"
	"github.com/spin-stack/go-tools/versions"
)

// A repository's commands, judged: pnpm is the one package manager, wherever a command is
// written - npm, npx, yarn and bun are refused even through the firewall - and what pnpm fetches it
// fetches through the firewall; what runs what is installed is left alone. Another manager's
// lockfile, and a package.json that does not name pnpm, are refused too. What only names a
// command - a description, a comment, a fixture - is no command.
func TestPnpmIsTheOnePackageManagerAndInstallsThroughTheFirewall(t *testing.T) {
	repo := t.TempDir()
	write := func(rel, body string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(repo, rel)), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(repo, rel), []byte(body), 0o600))
	}
	write(".github/workflows/ci.yml", `jobs:
  build:
    steps:
      - uses: actions/setup-node@v6
        with:
          cache: pnpm
      - run: task site
      - run: |
          cd site
          pnpm build
      - run: pnpm install
`)
	write(".github/actions/setup/action.yml", "runs:\n  steps:\n    - run: npx something\n      shell: bash\n")
	write("Taskfile.yml", `version: '3'
tasks:
  install:
    desc: pnpm install, through the firewall
    cmds:
      # pnpm install is what this was
      - go tool sfw pnpm install --frozen-lockfile
      - pnpm build
      - cmd: cd dashboard && pnpm --dir web install
      - pnpm -C dashboard exec vite build
  run:
    cmds:
      - |
        go run ./cmd/sfw pnpm add left-pad
        CI=1 pnpm dlx create-thing \
          --yes
  version:
    vars:
      V:
        sh: npm view astro version
`)
	write(".github/workflows/release.yaml", "jobs:\n  r:\n    steps:\n      - run: exec pnpm publish\n")
	write(".github/workflows/README.md", "    - run: pnpm install\n")
	write(".github/actions/setup/notes.yml", "run: npm ci\n")
	write(".github/actions/other/action.yaml", "runs:\n  steps:\n    - run: command yarn add x\n")
	write("docs/Taskfile.txt", "cmds:\n  - npm install\n")
	write("hack/setup.sh", "#!/bin/sh\nset -e\npnpx cowsay\nsfw pnpx cowsay\nsfw yarn install\nsfw\ngo tool sfw\nFOO=bar\ngo tool sfw pnpm -w --filter web add x\ncommand pnpm i\npnpm\nbunx thing\n")
	write("site/Dockerfile", "FROM node:22\nRUN corepack enable && pnpm install --frozen-lockfile\nRUN pnpm build\n")
	write("site/package.json", `{"name": "site", "packageManager": "pnpm@11.27.1"}`)
	write("old/package.json", `{"name": "old", "packageManager": "yarn@1.22.22"}`)
	write("old/yarn.lock", "")
	write("old/package-lock.json", "{}")
	write("README.md", "npm install\n")
	write("internal/x/testdata/Taskfile.yml", "tasks:\n  a:\n    cmds:\n      - npm install\n")
	write("internal/x/testdata/package-lock.json", "{}")
	write(".gitignore", "ignored/\n")
	write("ignored/install.sh", "npm install\n")
	git := exec.Command("git", "init", "-q", repo)
	out, err := git.CombinedOutput()
	require.NoError(t, err, "%s", out)

	findings, err := sfw.Check(repo)
	require.NoError(t, err)
	var got []string
	for _, f := range findings {
		got = append(got, fmt.Sprintf("%s:%d %s %d", f.File, f.Line, f.Command, f.Why))
	}
	firewall, other, lock, unpinned := sfw.Unfirewalled, sfw.NotPnpm, sfw.Lockfile, sfw.Unpinned
	assert.ElementsMatch(t, []string{
		fmt.Sprintf(".github/actions/setup/action.yml:3 npx something %d", other),
		fmt.Sprintf(".github/actions/other/action.yaml:3 yarn add x %d", other),
		fmt.Sprintf(".github/workflows/ci.yml:11 pnpm install %d", firewall),
		fmt.Sprintf("Taskfile.yml:9 pnpm --dir web install %d", firewall),
		fmt.Sprintf("Taskfile.yml:15 pnpm dlx create-thing --yes %d", firewall),
		fmt.Sprintf("Taskfile.yml:20 npm view astro version %d", other),
		fmt.Sprintf("hack/setup.sh:3 pnpx cowsay %d", firewall),
		fmt.Sprintf("hack/setup.sh:5 yarn install %d", other),
		fmt.Sprintf("hack/setup.sh:10 pnpm i %d", firewall),
		fmt.Sprintf("hack/setup.sh:12 bunx thing %d", other),
		fmt.Sprintf("site/Dockerfile:2 pnpm install --frozen-lockfile %d", firewall),
		fmt.Sprintf("old/package.json:0 package.json %d", unpinned),
		fmt.Sprintf("old/yarn.lock:0 yarn.lock %d", lock),
		fmt.Sprintf("old/package-lock.json:0 package-lock.json %d", lock),
	}, got)
	said := map[sfw.Why]string{}
	for _, f := range findings {
		said[f.Why] = f.String()
	}
	assert.Contains(t, said[firewall], "go tool sfw")
	assert.Contains(t, said[other], "never npm")
	assert.Contains(t, said[lock], "pnpm import")
	assert.Contains(t, said[unpinned], "packageManager")
}

// The firewall is downloaded once, run from the cache after, and fetched again when what the
// cache holds is not what the pin names; a download that is not the pinned bytes is refused and
// leaves nothing to run.
func TestTheBinaryIsThePinnedOne(t *testing.T) {
	binary := []byte("#!/bin/sh\necho firewall\n")
	var served atomic.Pointer[[]byte]
	served.Store(&binary)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write(*served.Load())
	}))
	defer server.Close()
	sum := sha256.Sum256(binary)
	v, err := versions.Parse([]byte(`- name: sfw-linux-amd64
  kind: download
  source: https://example.com/v{version}/sfw
  version: 1.0.0
  pin: ` + hex.EncodeToString(sum[:]) + `
  track: github-release https://github.com/SocketDev/sfw-free
`))
	require.NoError(t, err)
	e, err := sfw.Entry(v, "linux", "amd64")
	require.NoError(t, err)
	_, err = sfw.Entry(v, "windows", "arm64")
	assert.ErrorContains(t, err, "windows/arm64")
	// Served from here: versions.yaml takes only an https source, and the test serves http.
	e.Source = server.URL + "/v{version}/sfw"

	cache := t.TempDir()
	path, err := sfw.Binary(t.Context(), e, cache)
	require.NoError(t, err)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, binary, raw)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&0o100, "the binary cannot be run")

	_, err = sfw.Binary(t.Context(), e, cache)
	require.NoError(t, err)
	assert.Equal(t, int32(1), hits.Load(), "a cached binary was downloaded again")

	require.NoError(t, os.WriteFile(path, []byte("tampered"), 0o600))
	_, err = sfw.Binary(t.Context(), e, cache)
	require.NoError(t, err)
	assert.Equal(t, int32(2), hits.Load(), "a cached binary that is not the pin's was run")
	raw, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, binary, raw)

	other := []byte("something else")
	served.Store(&other)
	elsewhere := t.TempDir()
	_, err = sfw.Binary(t.Context(), e, elsewhere)
	require.ErrorContains(t, err, "not what versions.yaml pins")
	entries, err := os.ReadDir(elsewhere)
	require.NoError(t, err)
	assert.Empty(t, entries, "a refused download left something behind")
}
