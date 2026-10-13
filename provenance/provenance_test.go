package provenance_test

import (
	"maps"
	"os"
	"path/filepath"
	"testing"

	slsa "github.com/in-toto/attestation/go/predicates/provenance/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/spin-stack/go-tools/provenance"
	"github.com/spin-stack/go-tools/versions"
)

// A release workflow's environment, as GitHub sets it on a dispatch from main.
var run = map[string]string{
	"GITHUB_SERVER_URL":   "https://github.com",
	"GITHUB_REPOSITORY":   "spin-stack/spin",
	"GITHUB_WORKFLOW_REF": "spin-stack/spin/.github/workflows/release.yml@refs/heads/main",
	"GITHUB_REF":          "refs/heads/main",
	"GITHUB_SHA":          "0123456789abcdef0123456789abcdef01234567",
	"GITHUB_EVENT_NAME":   "workflow_dispatch",
	"RUNNER_ENVIRONMENT":  "github-hosted",
	"GITHUB_RUN_ID":       "38396720671",
	"GITHUB_RUN_ATTEMPT":  "1",
}

const pins = `
- name: qemu
  kind: download
  source: https://download.qemu.org/qemu-{version}.tar.xz
  version: 11.0.0
  pin: 1111111111111111111111111111111111111111111111111111111111111111
  track: tags https://gitlab.com/qemu-project/qemu.git
- name: debian
  kind: image
  source: debian
  version: trixie
  pin: sha256:2222222222222222222222222222222222222222222222222222222222222222
  track: digest
- name: alloy
  kind: git
  source: https://github.com/grafana/alloy
  version: v1.15.0
  pin: 3333333333333333333333333333333333333333
  archive: 4444444444444444444444444444444444444444444444444444444444444444
  track: github-release
- name: actionlint
  kind: module
  source: github.com/rhysd/actionlint/cmd/actionlint
  version: v1.7.12
  track: github-release rhysd/actionlint
- name: debian-snapshot
  kind: date
  version: 20261001T000000Z
  track: today
- name: uefivars
  kind: pypi
  source: uefivars
  version: 1.2.0
  track: pypi
`

func getenv(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

// The predicate of a run is the golden file: the workflow at its ref is the builder, the
// repository at its commit the first input, every pin after it by name, and nothing a clock says.
// SLSA's own types read it and find it valid. UPDATE_GOLDEN=1 writes it.
func TestARunsPredicateIsTheGolden(t *testing.T) {
	v, err := versions.Parse([]byte(pins))
	require.NoError(t, err)
	extra := []provenance.ResourceDescriptor{{Name: "base.qcow2", Digest: map[string]string{"sha256": "5555555555555555555555555555555555555555555555555555555555555555"}}}
	p, err := provenance.Build(getenv(run), "v20261013.1", v, extra)
	require.NoError(t, err)
	got, err := p.JSON()
	require.NoError(t, err)

	golden := filepath.Join("testdata", "release.json")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		require.NoError(t, os.WriteFile(golden, got, 0o600))
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))

	var parsed slsa.Provenance
	require.NoError(t, protojson.Unmarshal(got, &parsed))
	require.NoError(t, parsed.Validate())
	again, err := provenance.Build(getenv(run), "v20261013.1", v, extra)
	require.NoError(t, err)
	assert.Equal(t, p, again)
}

// Built by a reusable workflow, the builder is that workflow at the run's ref, and the dispatched
// one stays the build's workflow; a builder that is not a workflow file of the repository is no
// builder.
func TestABuilderIsTheReusableWorkflowAtTheRunsRef(t *testing.T) {
	p, err := provenance.Build(getenv(run), "v20261013.1", nil, nil)
	require.NoError(t, err)
	built, err := p.BuiltBy(".github/workflows/build-release.yml")
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/spin-stack/spin/.github/workflows/build-release.yml@refs/heads/main", built.RunDetails.Builder.ID)
	assert.Equal(t, ".github/workflows/release.yml", built.BuildDefinition.ExternalParameters.Workflow.Path)
	assert.Equal(t, "https://github.com/spin-stack/spin/.github/workflows/release.yml@refs/heads/main", p.RunDetails.Builder.ID)
	for _, path := range []string{"hack/build.yml", "build-release.yml", ".github/workflows/build-release.yml@refs/heads/x"} {
		_, err := p.BuiltBy(path)
		assert.Error(t, err, path)
	}
}

// An environment that is not a workflow run's, or a workflow ref that is not the repository's,
// is refused: provenance with no builder in it names nobody.
func TestAnEnvironmentThatIsNotARunIsRefused(t *testing.T) {
	without := func(k string) map[string]string {
		env := maps.Clone(run)
		delete(env, k)
		return env
	}
	with := func(k, v string) map[string]string {
		env := maps.Clone(run)
		env[k] = v
		return env
	}
	for name, tc := range map[string]struct {
		env     map[string]string
		version string
	}{
		"no commit":                    {env: without("GITHUB_SHA"), version: "v1"},
		"no run attempt":               {env: without("GITHUB_RUN_ATTEMPT"), version: "v1"},
		"a laptop":                     {env: map[string]string{}, version: "v1"},
		"no version":                   {env: run},
		"another repository's":         {env: with("GITHUB_WORKFLOW_REF", "other/repo/.github/workflows/release.yml@refs/heads/main"), version: "v1"},
		"a workflow ref with no ref":   {env: with("GITHUB_WORKFLOW_REF", "spin-stack/spin/.github/workflows/release.yml"), version: "v1"},
		"a file that is no workflow's": {env: with("GITHUB_WORKFLOW_REF", "spin-stack/spin/hack/release.yml@refs/heads/main"), version: "v1"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := provenance.Build(getenv(tc.env), tc.version, nil, nil)
			require.Error(t, err)
		})
	}
}
