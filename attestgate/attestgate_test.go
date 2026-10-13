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
		"a command over continued lines": {workflow: workflow("workflow_dispatch", "id-token: write", "", "",
			"      - name: Image\n        run: |\n          docker buildx build \\\n            --tag ghcr.io/x/y:v1 \\\n            --push \\\n            .\n"),
			problems: []string{"release.yml/release/Image: publishes with no provenance"}},
		"an attestation over continued lines": {workflow: workflow("workflow_dispatch", "id-token: write", "", "",
			"      - name: Attest\n        run: |\n          cosign attest-blob --yes \\\n            --type slsaprovenance1 --predicate p.json f\n", release)},
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
		"a step that attests what it uploads, under its own if": {workflow: workflow("push", "id-token: write", "", "",
			"      - name: Save\n        if: github.event_name == 'push'\n        run: task prebuilt:save\n"),
			gate: attestgate.Gate{UploadTasks: []string{"prebuilt:save"}, AttestTasks: []string{"prebuilt:save"}}},
		"a conditional step's attestation is not a later step's": {workflow: workflow("push", "id-token: write", "", "",
			"      - name: Save\n        if: github.event_name == 'push'\n        run: task prebuilt:save\n", release),
			gate:     attestgate.Gate{UploadTasks: []string{"prebuilt:save"}, AttestTasks: []string{"prebuilt:save"}},
			problems: []string{"release.yml/release/Release: publishes with no provenance"}},
		"a step that attests what it uploads, its failure passed": {workflow: workflow("push", "id-token: write", "", "",
			"      - name: Save\n        continue-on-error: true\n        run: task prebuilt:save\n"),
			gate:     attestgate.Gate{UploadTasks: []string{"prebuilt:save"}, AttestTasks: []string{"prebuilt:save"}},
			problems: []string{"release.yml/release/Save: an attestation that may not happen"}},
		"a task that attests only what it uploads attests no later step": {workflow: workflow("workflow_dispatch", "id-token: write", "", "",
			"      - name: Save\n        run: task prebuilt:save\n", "      - name: Publish\n        run: task publish\n"),
			gate:     attestgate.Gate{UploadTasks: []string{"prebuilt:save", "publish"}, SelfAttesting: []string{"prebuilt:save"}},
			problems: []string{"release.yml/release/Publish: publishes with no provenance"}},
		"a task that attests only what it uploads": {workflow: workflow("workflow_dispatch", "id-token: write", "", "",
			"      - name: Save\n        run: task prebuilt:save\n"),
			gate: attestgate.Gate{UploadTasks: []string{"prebuilt:save"}, SelfAttesting: []string{"prebuilt:save"}}},
		"a task that attests only what it uploads, under a condition": {workflow: workflow("push", "id-token: write", "", "",
			"      - name: Save\n        if: github.event_name == 'push'\n        run: task prebuilt:save\n"),
			gate: attestgate.Gate{UploadTasks: []string{"prebuilt:save"}, SelfAttesting: []string{"prebuilt:save"}}},
		"an SBOM required and attested": {workflow: workflow("workflow_dispatch", "id-token: write", "", "",
			attest, "      - name: SBOM\n        run: cosign attest-blob --yes --type cyclonedx --predicate f.cdx.json f\n", release),
			gate: attestgate.Gate{RequireSBOM: true}},
		"an SBOM required and not attested": {workflow: clean, gate: attestgate.Gate{RequireSBOM: true},
			problems: []string{"release.yml/release/Release: publishes with no SBOM"}},
		"an SBOM not required": {workflow: clean},
		"an SBOM after the upload": {workflow: workflow("workflow_dispatch", "id-token: write", "", "",
			attest, release, "      - name: SBOM\n        run: cosign attest-blob --yes --type cyclonedx --predicate f.cdx.json f\n"),
			gate:     attestgate.Gate{RequireSBOM: true},
			problems: []string{"release.yml/release/Release: publishes with no SBOM"}},
		"an SBOM is no provenance": {workflow: workflow("workflow_dispatch", "id-token: write", "", "",
			"      - name: SBOM\n        run: cosign attest-blob --yes --type cyclonedx --predicate f.cdx.json f\n", release),
			gate:     attestgate.Gate{RequireSBOM: true},
			problems: []string{"release.yml/release/Release: publishes with no provenance"}},
		"a task attests both": {workflow: workflow("workflow_dispatch", "id-token: write", "", "",
			"      - name: Sign\n        run: task images:sign\n", "      - name: Publish\n        run: task publish\n"),
			gate: attestgate.Gate{RequireSBOM: true, UploadTasks: []string{"publish"}, AttestTasks: []string{"images:sign"}}},
		"a self-attesting task attests both": {workflow: workflow("workflow_dispatch", "id-token: write", "", "",
			"      - name: Save\n        run: task prebuilt:save\n"),
			gate: attestgate.Gate{RequireSBOM: true, UploadTasks: []string{"prebuilt:save"}, SelfAttesting: []string{"prebuilt:save"}}},
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

// With builders, what is published is published from them alone: reusable workflows that run only
// when called, in their own repository, called only by a job on main that gives them a token to
// sign with, and held inside to the rules every publishing job is held to.
func TestABuilderIsWhereEveryUploadIs(t *testing.T) {
	const main = "github.ref == 'refs/heads/main'"
	caller := func(on, perms, jobIf string) string {
		w := "name: release\non: " + on + "\n"
		if perms != "" {
			w += "permissions:\n  " + perms + "\n"
		}
		w += "jobs:\n  release:\n"
		if jobIf != "" {
			w += "    if: " + jobIf + "\n"
		}
		return w + "    uses: ./.github/workflows/build-release.yml\n"
	}
	good := caller("workflow_dispatch", "id-token: write", main)
	const own = "github.repository == 'spin-stack/x'"
	builder := workflow("workflow_call", "id-token: write", own, "", build, attest, release)
	gate := attestgate.Gate{MainOnly: []string{"release.yml"}, Builders: []string{"build-release.yml"}, Repository: "spin-stack/x"}
	// CI on a push to main, calling a second builder for what it saves.
	ci := "name: ci\non: [push, pull_request]\npermissions:\n  id-token: write\njobs:\n  save:\n    if: " + main +
		"\n    uses: ./.github/workflows/build-packs.yml\n"
	two := attestgate.Gate{MainOnly: []string{"release.yml"}, Builders: []string{"build-release.yml", "build-packs.yml"}, Repository: "spin-stack/x"}
	for name, tc := range map[string]struct {
		files    map[string]string
		gate     attestgate.Gate
		problems []string
	}{
		"called from main":  {files: map[string]string{"release.yml": good, "build-release.yml": builder}, gate: gate},
		"no builder at all": {files: map[string]string{"release.yml": workflow("workflow_dispatch", "id-token: write", main, "", attest, release)}, gate: attestgate.Gate{MainOnly: []string{"release.yml"}}},
		"an upload in the caller": {files: map[string]string{"release.yml": workflow("workflow_dispatch", "id-token: write", main, "", attest, release), "build-release.yml": builder},
			gate: gate, problems: []string{"release.yml/release/Release: publishes outside the builders [build-release.yml]"}},
		"an upload in another workflow": {files: map[string]string{"release.yml": good, "build-release.yml": builder,
			"nightly.yml": workflow("schedule", "id-token: write", "", "", attest, orasPush)},
			gate: gate, problems: []string{"nightly.yml/release/Push: publishes outside the builders"}},
		"an excused upload elsewhere": {files: map[string]string{"release.yml": good, "build-release.yml": builder,
			"ci.yml": workflow("push", "", "", "", "      - name: Logs\n        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1\n")},
			gate: attestgate.Gate{MainOnly: []string{"release.yml"}, Builders: []string{"build-release.yml"}, Repository: "spin-stack/x", NotPublished: map[string]string{"ci.yml/release/Logs": "the run's logs"}}},
		"two builders, one called from CI on main": {files: map[string]string{"release.yml": good, "build-release.yml": builder, "ci.yml": ci,
			"build-packs.yml": workflow("workflow_call", "id-token: write", own, "", build, attest, orasPush)}, gate: two},
		"a builder called from CI off main": {files: map[string]string{"release.yml": good, "build-release.yml": builder,
			"ci.yml":          strings.Replace(ci, "    if: "+main+"\n", "", 1),
			"build-packs.yml": workflow("workflow_call", "id-token: write", own, "", build, attest, orasPush)},
			gate: two, problems: []string{"ci.yml/save: calls a builder with no if: github.ref == 'refs/heads/main'"}},
		"a reusable workflow that is not a builder publishes": {files: map[string]string{"release.yml": good, "build-release.yml": builder, "ci.yml": ci,
			"build-packs.yml": workflow("workflow_call", "id-token: write", own, "", build, attest, orasPush)},
			gate: gate, problems: []string{"build-packs.yml/release/Push: publishes outside the builders"}},
		"a builder that runs by itself": {files: map[string]string{"release.yml": good,
			"build-release.yml": workflow("[workflow_call, workflow_dispatch]", "id-token: write", own, "", attest, release)},
			gate: gate, problems: []string{"build-release.yml: the builder is on: [workflow_call workflow_dispatch]"}},
		"a builder on something else": {files: map[string]string{"release.yml": good,
			"build-release.yml": workflow("push", "id-token: write", own, "", attest, release)},
			gate: gate, problems: []string{"build-release.yml: the builder is on: [push]"}},
		"a builder there is not": {files: map[string]string{"release.yml": good}, gate: gate,
			problems: []string{"build-release.yml: Builders names a workflow there is not"}},
		"a caller from any ref": {files: map[string]string{"release.yml": caller("workflow_dispatch", "id-token: write", ""), "build-release.yml": builder},
			gate: gate, problems: []string{"release.yml/release: a release job with no if", "release.yml/release: calls a builder with no if: github.ref"}},
		"a caller with no token to give": {files: map[string]string{"release.yml": caller("workflow_dispatch", "contents: write", main), "build-release.yml": builder},
			gate: gate, problems: []string{"release.yml/release: calls the builder with no id-token: write"}},
		"another workflow's uses is not the builder": {files: map[string]string{"release.yml": good, "build-release.yml": builder,
			"lint.yml": strings.Replace(caller("push", "", ""), "build-release.yml", "lint-reusable.yml", 1)}, gate: gate},
		"a builder that publishes unattested": {files: map[string]string{"release.yml": good,
			"build-release.yml": workflow("workflow_call", "id-token: write", own, "", build, release)},
			gate: gate, problems: []string{"build-release.yml/release/Release: publishes with no provenance"}},
		"a builder that runs in any repository": {files: map[string]string{"release.yml": good,
			"build-release.yml": workflow("workflow_call", "id-token: write", "", "", build, attest, release)},
			gate: gate, problems: []string{"build-release.yml/release: a builder job with no if: github.repository == 'spin-stack/x'"}},
		"a builder that runs in another repository": {files: map[string]string{"release.yml": good,
			"build-release.yml": workflow("workflow_call", "id-token: write", "github.repository == 'spin-stack/y'", "", build, attest, release)},
			gate: gate, problems: []string{"build-release.yml/release: a builder job with no if: github.repository == 'spin-stack/x'"}},
		"a builder with no repository": {files: map[string]string{"release.yml": good, "build-release.yml": builder},
			gate:     attestgate.Gate{MainOnly: []string{"release.yml"}, Builders: []string{"build-release.yml"}},
			problems: []string{"build-release.yml/release: a builder job with no if: github.repository == ''", "Builders with no Repository"}},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, ".github", "workflows")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			for f, body := range tc.files {
				require.NoError(t, os.WriteFile(filepath.Join(dir, f), []byte(body), 0o600))
			}
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
