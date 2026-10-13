package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/provenance"
)

var run1 = map[string]string{
	"GITHUB_SERVER_URL":   "https://github.com",
	"GITHUB_REPOSITORY":   "spin-stack/machine",
	"GITHUB_WORKFLOW_REF": "spin-stack/machine/.github/workflows/release.yml@refs/heads/main",
	"GITHUB_REF":          "refs/heads/main",
	"GITHUB_SHA":          "0123456789abcdef0123456789abcdef01234567",
	"GITHUB_EVENT_NAME":   "workflow_dispatch",
	"RUNNER_ENVIRONMENT":  "github-hosted",
	"GITHUB_RUN_ID":       "1",
	"GITHUB_RUN_ATTEMPT":  "1",
}

const pins = `
- name: debian-snapshot
  kind: date
  version: 20261001T000000Z
  track: today
`

// In a run it writes the predicate of the run and the pins; with no -out, no versions.yaml it can
// read, or no run around it, it writes nothing and says 2.
func TestItWritesTheRunsPredicateOrNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		env     map[string]string
		args    func(dir string) []string
		pins    string
		written bool
		builder string
	}{
		"in a run": {env: run1, pins: pins, written: true,
			args: func(dir string) []string { return []string{"-version", "v1", "-out", filepath.Join(dir, "p.json")} }},
		"no -out": {env: run1, pins: pins,
			args: func(string) []string { return []string{"-version", "v1"} }},
		"no run": {env: map[string]string{}, pins: pins,
			args: func(dir string) []string { return []string{"-version", "v1", "-out", filepath.Join(dir, "p.json")} }},
		"no versions.yaml": {env: run1,
			args: func(dir string) []string { return []string{"-version", "v1", "-out", filepath.Join(dir, "p.json")} }},
		"built by a reusable workflow": {env: run1, pins: pins, written: true, builder: "https://github.com/spin-stack/machine/.github/workflows/build-release.yml@refs/heads/main",
			args: func(dir string) []string {
				return []string{"-version", "v1", "-builder", ".github/workflows/build-release.yml", "-out", filepath.Join(dir, "p.json")}
			}},
		"a builder that is no workflow": {env: run1, pins: pins,
			args: func(dir string) []string {
				return []string{"-version", "v1", "-builder", "build-release.yml", "-out", filepath.Join(dir, "p.json")}
			}},
		"an unknown flag": {env: run1, pins: pins,
			args: func(string) []string { return []string{"-subject", "x"} }},
		"an -out it cannot write": {env: run1, pins: pins,
			args: func(dir string) []string {
				return []string{"-version", "v1", "-out", filepath.Join(dir, "no", "p.json")}
			}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			if tc.pins != "" {
				require.NoError(t, os.WriteFile("versions.yaml", []byte(tc.pins), 0o600))
			}
			var errs bytes.Buffer
			status := run(func(k string) string { return tc.env[k] }, tc.args(dir), &errs)
			got, err := os.ReadFile(filepath.Join(dir, "p.json"))
			if !tc.written {
				assert.Equal(t, 2, status)
				assert.True(t, os.IsNotExist(err))
				return
			}
			require.Equal(t, 0, status, errs.String())
			require.NoError(t, err)
			var p provenance.Predicate
			require.NoError(t, json.Unmarshal(got, &p))
			assert.Equal(t, "v1", p.BuildDefinition.ExternalParameters.Version)
			assert.Equal(t, "debian-snapshot", p.BuildDefinition.ResolvedDependencies[1].Name)
			builder := tc.builder
			if builder == "" {
				builder = "https://github.com/spin-stack/machine/.github/workflows/release.yml@refs/heads/main"
			}
			assert.Equal(t, builder, p.RunDetails.Builder.ID)
		})
	}
}
