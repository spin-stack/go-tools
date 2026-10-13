// Package provenance writes the SLSA v1 provenance predicate of a build that ran in a GitHub
// Actions workflow: which workflow, at which commit, asked by what, and every input its
// repository's versions.yaml pins. cosign attest-blob --type slsaprovenance1 wraps it in a
// statement over the files and signs that, so what a verifier reads is who built a file and from
// what, signed by the workflow that did.
//
// The predicate is a function of the workflow's environment and versions.yaml only: it carries no
// clock, so the same run answers the same bytes and a reproduced build its own.
package provenance

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spin-stack/go-tools/versions"
)

// BuildType is what a verifier reads the parameters by: a GitHub Actions workflow, run as
// go-tools says, with versions.yaml its inputs.
const BuildType = "https://github.com/spin-stack/go-tools/provenance/github-workflow/v1"

// Predicate is SLSA's provenance v1, as far as a workflow fills it.
type Predicate struct {
	BuildDefinition BuildDefinition `json:"buildDefinition"`
	RunDetails      RunDetails      `json:"runDetails"`
}

// BuildDefinition is what was asked and what it was built from.
type BuildDefinition struct {
	BuildType            string               `json:"buildType"`
	ExternalParameters   ExternalParameters   `json:"externalParameters"`
	InternalParameters   InternalParameters   `json:"internalParameters"`
	ResolvedDependencies []ResourceDescriptor `json:"resolvedDependencies"`
}

// ExternalParameters are what whoever started the run chose: the workflow, and the release.
type ExternalParameters struct {
	Workflow Workflow `json:"workflow"`
	Version  string   `json:"version"`
}

// Workflow is the file a run executed, and the ref it was run on.
type Workflow struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Ref        string `json:"ref"`
}

// InternalParameters are the platform's: what started the run and on what kind of runner.
type InternalParameters struct {
	EventName         string `json:"eventName"`
	RunnerEnvironment string `json:"runnerEnvironment"`
}

// RunDetails is who built it and which run.
type RunDetails struct {
	Builder  Builder  `json:"builder"`
	Metadata Metadata `json:"metadata"`
}

// Builder is the workflow, at its ref: what a verifier holds the signature's identity to.
type Builder struct {
	ID string `json:"id"`
}

// Metadata names the run; it has no start or end time, which would make every run's bytes its own.
type Metadata struct {
	InvocationID string `json:"invocationId"`
}

// ResourceDescriptor is one input: what it is, where it is, and the digests that hold it.
type ResourceDescriptor struct {
	Name        string            `json:"name,omitempty"`
	URI         string            `json:"uri,omitempty"`
	Digest      map[string]string `json:"digest,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// The variables a workflow's run sets, and a predicate needs every one of.
var required = []string{
	"GITHUB_SERVER_URL", "GITHUB_REPOSITORY", "GITHUB_WORKFLOW_REF", "GITHUB_REF", "GITHUB_SHA",
	"GITHUB_EVENT_NAME", "RUNNER_ENVIRONMENT", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT",
}

// Build is the predicate of the run env describes (os.Getenv in a workflow) building version,
// from the repository at its commit, pins and extra. It refuses an environment that is not a
// workflow's: provenance with no builder in it says nothing.
func Build(env func(string) string, version string, pins *versions.Versions, extra []ResourceDescriptor) (Predicate, error) {
	var missing []string
	for _, k := range required {
		if env(k) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return Predicate{}, fmt.Errorf("provenance: not a GitHub Actions run: %s unset", strings.Join(missing, ", "))
	}
	if version == "" {
		return Predicate{}, errors.New("provenance: no version")
	}
	repo, server := env("GITHUB_REPOSITORY"), env("GITHUB_SERVER_URL")
	// GITHUB_WORKFLOW_REF is owner/repo/.github/workflows/file.yml@ref.
	file, ref, ok := strings.Cut(strings.TrimPrefix(env("GITHUB_WORKFLOW_REF"), repo+"/"), "@")
	if !ok || !strings.HasPrefix(file, ".github/workflows/") {
		return Predicate{}, fmt.Errorf("provenance: GITHUB_WORKFLOW_REF %q is not a workflow of %s", env("GITHUB_WORKFLOW_REF"), repo)
	}
	deps := []ResourceDescriptor{{
		URI:    "git+" + server + "/" + repo + "@" + env("GITHUB_REF"),
		Digest: map[string]string{"gitCommit": env("GITHUB_SHA")},
	}}
	if pins != nil {
		for _, e := range pins.Entries {
			deps = append(deps, Pinned(e))
		}
	}
	deps = append(deps, extra...)
	// The repository first, the rest by name: the order is the bytes, and nothing about a run
	// decides it.
	slices.SortStableFunc(deps[1:], func(a, b ResourceDescriptor) int { return strings.Compare(a.Name, b.Name) })
	return Predicate{
		BuildDefinition: BuildDefinition{
			BuildType: BuildType,
			ExternalParameters: ExternalParameters{
				Workflow: Workflow{Repository: server + "/" + repo, Path: file, Ref: ref},
				Version:  version,
			},
			InternalParameters:   InternalParameters{EventName: env("GITHUB_EVENT_NAME"), RunnerEnvironment: env("RUNNER_ENVIRONMENT")},
			ResolvedDependencies: deps,
		},
		RunDetails: RunDetails{
			Builder:  Builder{ID: server + "/" + repo + "/" + file + "@" + ref},
			Metadata: Metadata{InvocationID: server + "/" + repo + "/actions/runs/" + env("GITHUB_RUN_ID") + "/attempts/" + env("GITHUB_RUN_ATTEMPT")},
		},
	}, nil
}

// Pinned is a versions.yaml entry as an input: its name, where it is, and the digest that holds
// it - none for a module, which the checksum database holds by its version, or a date.
func Pinned(e versions.Entry) ResourceDescriptor {
	rd := ResourceDescriptor{Name: e.Name, Annotations: map[string]string{"version": e.Version}}
	switch e.Kind {
	case versions.Image:
		rd.URI = "docker://" + e.Source + ":" + e.Version
		rd.Digest = map[string]string{"sha256": strings.TrimPrefix(e.Pin, "sha256:")}
	case versions.Git:
		rd.URI = "git+" + e.Source + "@" + e.Version
		rd.Digest = map[string]string{"gitCommit": e.Pin}
		if e.Archive != "" {
			rd.Annotations["archiveSha256"] = e.Archive
		}
	case versions.Download:
		rd.URI = e.URL()
		rd.Digest = map[string]string{"sha256": e.Pin}
	case versions.Module:
		rd.URI = "pkg:golang/" + e.Source + "@" + e.Version
	case versions.PyPI:
		rd.URI = "pkg:pypi/" + e.Source + "@" + e.Version
	case versions.Date:
	}
	return rd
}

// JSON is the predicate as cosign attest-blob --predicate reads it, indented: map keys in order,
// so the same predicate is the same bytes.
func (p Predicate) JSON() ([]byte, error) {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
