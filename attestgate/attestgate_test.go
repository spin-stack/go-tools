package attestgate_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/attestgate"
)

const (
	attest   = "      - name: Attest\n        run: cosign attest-blob --yes --type slsaprovenance1 --predicate p.json dist/f\n"
	release  = "      - name: Release\n        uses: softprops/action-gh-release@efb35369e0ad2afab669f228072c1b0d510eae64 # v3.0.3\n"
	orasPush = "      - name: Push\n        run: |\n          oras push ghcr.io/x/y:v1 f\n"
	build    = "      - name: Build\n        run: task build\n"
)

// release.yml with on, the workflow's permissions, and one job, its if and permissions and steps.
func workflow(on, perms, jobIf, jobPerms string, steps ...string) string {
	var b strings.Builder
	b.WriteString("name: release\non: " + on + "\n")
	if perms != "" {
		b.WriteString("permissions:\n  " + perms + "\n")
	}
	b.WriteString("jobs:\n  release:\n    runs-on: ubuntu-latest\n")
	if jobIf != "" {
		b.WriteString("    if: " + jobIf + "\n")
	}
	if jobPerms != "" {
		b.WriteString("    permissions:\n      " + jobPerms + "\n")
	}
	b.WriteString("    steps:\n")
	for _, s := range steps {
		b.WriteString(s)
	}
	return b.String()
}

// Each workflow that publishes what it did not attest - or attests with no token to sign, or
// with a step that may not run - fails with the step named; one that attests first passes. The
// release workflows run only from main; an excuse must excuse something, and say why.
func TestAnUploadNeedsAnAttestationBeforeIt(t *testing.T) {
	const main = "github.ref == 'refs/heads/main'"
	clean := workflow("workflow_dispatch", "id-token: write", main, "", build, attest, release)
	for name, tc := range map[string]struct {
		workflow string
		gate     attestgate.Gate
		problems []string
	}{
		"attested first":    {workflow: clean},
		"nothing published": {workflow: workflow("push", "", "", "", build)},
		"no attestation": {workflow: workflow("workflow_dispatch", "id-token: write", "", "", build, release),
			problems: []string{"release.yml/release/Release: publishes with no provenance"}},
		"attested after": {workflow: workflow("workflow_dispatch", "id-token: write", "", "", release, attest),
			problems: []string{"release.yml/release/Release: publishes with no provenance"}},
		"a command that publishes": {workflow: workflow("workflow_dispatch", "id-token: write", "", "", orasPush),
			problems: []string{"release.yml/release/Push: publishes with no provenance"}},
		"no id-token": {workflow: workflow("workflow_dispatch", "contents: write", "", "", attest, release),
			problems: []string{"release.yml/release/Release: attests with no id-token"}},
		"the job's permissions replace the workflow's": {workflow: workflow("workflow_dispatch", "id-token: write", "", "contents: write", attest, release),
			problems: []string{"release.yml/release/Release: attests with no id-token"}},
		"the job's own id-token": {workflow: workflow("workflow_dispatch", "", "", "id-token: write", attest, release)},
		"id-token among others":  {workflow: workflow("workflow_dispatch", "contents: write\n  packages: write\n  id-token: write", "", "", attest, release)},
		"write-all":              {workflow: strings.Replace(workflow("workflow_dispatch", "", "", "", attest, release), "jobs:", "permissions: write-all\njobs:", 1)},
		"an attestation that may not run": {workflow: workflow("workflow_dispatch", "id-token: write", "", "",
			strings.Replace(attest, "        run:", "        if: false\n        run:", 1), release),
			problems: []string{"release.yml/release/Attest: an attestation that may not happen", "release.yml/release/Release: publishes with no provenance"}},
		"an attestation whose failure passes": {workflow: workflow("workflow_dispatch", "id-token: write", "", "",
			strings.Replace(attest, "        run:", "        continue-on-error: true\n        run:", 1), release),
			problems: []string{"release.yml/release/Attest: an attestation that may not happen", "release.yml/release/Release: publishes with no provenance"}},
		"continue-on-error false is no condition": {workflow: workflow("workflow_dispatch", "id-token: write", "", "",
			strings.Replace(attest, "        run:", "        continue-on-error: false\n        run:", 1), release)},
		"a signature is no provenance": {workflow: workflow("workflow_dispatch", "id-token: write", "", "",
			"      - name: Sign\n        run: cosign sign-blob --yes f\n", release),
			problems: []string{"release.yml/release/Release: publishes with no provenance"}},
		"tasks that publish and attest": {workflow: workflow("workflow_dispatch", "id-token: write", "", "",
			"      - name: Sign\n        run: task images:sign\n", "      - name: Publish\n        run: task -v publish ROLE=cp\n"),
			gate: attestgate.Gate{UploadTasks: []string{"publish"}, AttestTasks: []string{"images:sign"}}},
		"a task that publishes": {workflow: workflow("workflow_dispatch", "id-token: write", "", "", "      - name: Publish\n        run: task publish\n"),
			gate:     attestgate.Gate{UploadTasks: []string{"publish"}},
			problems: []string{"release.yml/release/Publish: publishes with no provenance"}},
		"another task's name is not it": {workflow: workflow("workflow_dispatch", "id-token: write", "", "", "      - name: Publish\n        run: task publish:check\n"),
			gate: attestgate.Gate{UploadTasks: []string{"publish"}}},
		"excused": {workflow: workflow("push", "", "", "", "      - name: Logs\n        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1\n"),
			gate: attestgate.Gate{NotPublished: map[string]string{"release.yml/release/Logs": "the run's logs, for a person"}}},
		"an excuse for nothing": {workflow: clean, gate: attestgate.Gate{NotPublished: map[string]string{"release.yml/release/Gone": "it was"}},
			problems: []string{"release.yml/release/Gone: NotPublished names no upload step"}},
		"an excuse with no reason": {workflow: workflow("push", "", "", "", "      - name: Logs\n        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1\n"),
			gate:     attestgate.Gate{NotPublished: map[string]string{"release.yml/release/Logs": " "}},
			problems: []string{"release.yml/release/Logs: NotPublished with no reason"}},
		"main only": {workflow: clean, gate: attestgate.Gate{MainOnly: []string{"release.yml"}}},
		"main only, pushed": {workflow: workflow("[push, workflow_dispatch]", "id-token: write", main, "", attest, release),
			gate:     attestgate.Gate{MainOnly: []string{"release.yml"}},
			problems: []string{"release.yml: a release workflow with a push trigger"}},
		"main only, pushed tags": {workflow: workflow("\n  workflow_dispatch:\n  schedule:\n    - cron: '0 3 * * *'\n  push:\n    tags: ['v*']", "id-token: write", main, "", attest, release),
			gate:     attestgate.Gate{MainOnly: []string{"release.yml"}},
			problems: []string{"release.yml: a release workflow with a push trigger"}},
		"main only, a job from any ref": {workflow: workflow("workflow_dispatch", "id-token: write", "", "", attest, release),
			gate:     attestgate.Gate{MainOnly: []string{"release.yml"}},
			problems: []string{"release.yml/release: a release job with no if"}},
		"main only, a workflow there is not": {workflow: clean, gate: attestgate.Gate{MainOnly: []string{"cut.yml"}},
			problems: []string{"cut.yml: MainOnly names a workflow there is not"}},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, ".github", "workflows")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "release.yml"), []byte(tc.workflow), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "README"), []byte("not a workflow"), 0o600))
			tc.gate.Root = root
			err := tc.gate.Check()
			if len(tc.problems) == 0 {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			lines := strings.Split(err.Error(), "\n")[1:]
			require.Len(t, lines, len(tc.problems), err.Error())
			for i, p := range tc.problems {
				assert.Contains(t, lines[i], p)
			}
		})
	}
}

// A repository with no workflows, or one that is not YAML, is no clean answer.
func TestWorkflowsItCannotReadAreAnError(t *testing.T) {
	require.Error(t, attestgate.Gate{Root: t.TempDir()}.Check())
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.yml"), []byte("jobs: ["), 0o600))
	require.Error(t, attestgate.Gate{Root: root}.Check())
}

// Every file but bundles and the excepted needs each required bundle beside it.
func TestEveryFileToUploadIsAttested(t *testing.T) {
	for name, tc := range map[string]struct {
		files   []string
		require []string
		except  []string
		missing []string
	}{
		"attested":                     {files: []string{"a.tar.gz", "a.tar.gz.provenance.sigstore.json"}, require: []string{"provenance"}},
		"one is not":                   {files: []string{"a", "a.provenance.sigstore.json", "b"}, require: []string{"provenance"}, missing: []string{"b.provenance.sigstore.json"}},
		"a signature is no provenance": {files: []string{"a", "a.sigstore.json"}, require: []string{"provenance"}, missing: []string{"a.provenance.sigstore.json"}},
		"both kinds":                   {files: []string{"a", "a.provenance.sigstore.json"}, require: []string{"provenance", "cdx"}, missing: []string{"a.cdx.sigstore.json"}},
		"excepted":                     {files: []string{"checksums.txt", "a.sha256"}, require: []string{"provenance"}, except: []string{"checksums.txt", "*.sha256"}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tc.files {
				require.NoError(t, os.WriteFile(filepath.Join(dir, f), nil, 0o600))
			}
			require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o755))
			missing, err := attestgate.Files(dir, tc.require, tc.except)
			require.NoError(t, err)
			assert.Equal(t, tc.missing, missing)
		})
	}
	_, err := attestgate.Files(filepath.Join(t.TempDir(), "none"), []string{"provenance"}, nil)
	require.Error(t, err)
	_, err = attestgate.Files(t.TempDir(), []string{"provenance"}, []string{"["})
	require.NoError(t, err, "a bad pattern over no files is never matched")
}
