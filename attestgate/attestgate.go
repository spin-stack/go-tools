// Package attestgate holds a repository's workflows to one rule: a step that publishes what a
// release ships runs after a step, in the same job and under the same token, that attested it
// with cosign attest-blob --type slsaprovenance1. Gate reads the workflows, as a test of the
// repository runs it; Files is the same rule at the moment of the upload, over the files
// themselves, which the YAML cannot name.
package attestgate

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Gate is a repository's workflows and what its tasks do: the Taskfile's names that publish and
// that attest, which the YAML only runs by name.
type Gate struct {
	// Root is the repository, relative to the test that runs the gate.
	Root string
	// UploadTasks and AttestTasks are task names a step runs (`task <name>`) that publish, or
	// that attest what is published.
	UploadTasks, AttestTasks []string
	// SelfAttesting are task names that attest only what they upload themselves - a task that
	// packs, attests and pushes - so a step running one is attested and attests no later step.
	SelfAttesting []string
	// RequireSBOM is an upload needing an SBOM attested before it too (cosign attest-blob --type
	// cyclonedx), as it needs its provenance: off until a repository writes its SBOMs.
	RequireSBOM bool
	// NotPublished are the steps that look like an upload and ship nothing anyone installs - a
	// CI artifact, a measurement - each "<workflow file>/<job>/<step name or id>" with its
	// reason. One that matches no upload step fails, so the list cannot outlive what it excused.
	NotPublished map[string]string
	// MainOnly are the workflow files that cut a release: none has a push trigger, and every job
	// in one runs only on refs/heads/main.
	MainOnly []string
	// Builders are the reusable workflows every publishing step is in, by their file names: each
	// on: workflow_call and nothing else, so it runs only as a job of a workflow that calls it with
	// uses:, and only from a job that runs on refs/heads/main. The certificate a step in one signs
	// with names that builder, not the caller, so a verifier pinning it pins the code that built and
	// attested; a caller that published by itself would sign as itself and be outside the pin. None:
	// a release workflow publishes itself.
	Builders []string
	// Repository is the repository the builders are, owner/name, which every job of a builder runs
	// only in (if: github.repository == '<Repository>'): any repository can call a public reusable
	// workflow, and its run's certificate names the builder at the ref it called, over what that
	// repository checked out. Required with Builders.
	Repository string
}

// checkBuilders is what is wrong with the Builders named, given the workflow files there are.
func (g Gate) checkBuilders(files map[string]bool) []string {
	if len(g.Builders) == 0 {
		return nil
	}
	var problems []string
	for _, b := range g.Builders {
		if !files[b] {
			problems = append(problems, b+": Builders names a workflow there is not")
		}
	}
	if !strings.Contains(g.Repository, "/") {
		problems = append(problems, "Builders with no Repository (owner/name) to run only in")
	}
	return problems
}

func (g Gate) isBuilder(file string) bool { return slices.Contains(g.Builders, file) }

// ownRepository is whether a job's if: holds it to the builder's repository.
func (g Gate) ownRepository(cond string) bool {
	return g.Repository != "" &&
		regexp.MustCompile(`github\.repository\s*==\s*'`+regexp.QuoteMeta(g.Repository)+`'`).MatchString(cond)
}

// callsBuilder is whether a job's uses: is a builder, as a workflow of the same repository names it.
func (g Gate) callsBuilder(j job) bool {
	file, ok := strings.CutPrefix(j.Uses, "./.github/workflows/")
	return ok && g.isBuilder(file)
}

type workflow struct {
	On          yaml.Node      `yaml:"on"`
	Permissions yaml.Node      `yaml:"permissions"`
	Jobs        map[string]job `yaml:"jobs"`
}

type job struct {
	If          string    `yaml:"if"`
	Uses        string    `yaml:"uses"`
	Permissions yaml.Node `yaml:"permissions"`
	Steps       []step    `yaml:"steps"`
}

type step struct {
	Name            string `yaml:"name"`
	ID              string `yaml:"id"`
	Uses            string `yaml:"uses"`
	Run             string `yaml:"run"`
	If              string `yaml:"if"`
	ContinueOnError any    `yaml:"continue-on-error"`
}

// The actions and commands that put a file where something outside the run reads it.
var (
	uploadActions  = []string{"softprops/action-gh-release@", "actions/upload-artifact@"}
	uploadCommands = regexp.MustCompile(`\boras\s+push\b|\bgh\s+release\s+(upload|create)\b|\bdocker\s+push\b|\bbuildx\b[^\n]*--push\b|\baws\s+s3\s+(cp|sync)\b`)
	attestCommand  = regexp.MustCompile(`\bcosign\s+attest-blob\b[^\n]*--type[ =]slsaprovenance1\b`)
	sbomCommand    = regexp.MustCompile(`\bcosign\s+attest-blob\b[^\n]*--type[ =]cyclonedx\b`)
	mainRef        = regexp.MustCompile(`github\.ref\s*==\s*'refs/heads/main'`)
)

// runsTask is whether run starts `task <name>`, flags before the name or not.
func runsTask(run string, names []string) bool {
	for _, n := range names {
		if regexp.MustCompile(`\btask\s+(-\S+\s+)*` + regexp.QuoteMeta(n) + `(\s|$)`).MatchString(run) {
			return true
		}
	}
	return false
}

func (s step) key() string {
	if s.Name != "" {
		return s.Name
	}
	return s.ID
}

func (g Gate) uploads(s step) bool {
	for _, a := range uploadActions {
		if strings.HasPrefix(s.Uses, a) {
			return true
		}
	}
	return uploadCommands.MatchString(s.command()) || runsTask(s.command(), g.UploadTasks)
}

// covers is which attestations a step makes: provenance, an SBOM. A task's are both: what it
// attests is its package's tests to hold.
type covers struct{ provenance, sbom bool }

func (c covers) any() bool { return c.provenance || c.sbom }

func (c covers) or(d covers) covers { return covers{c.provenance || d.provenance, c.sbom || d.sbom} }

func (g Gate) attests(s step) covers {
	task := runsTask(s.command(), g.AttestTasks)
	return covers{provenance: attestCommand.MatchString(s.command()) || task, sbom: sbomCommand.MatchString(s.command()) || task}
}

// command is the step's script with each backslash-continued line joined to the next, as the
// shell reads it: `docker buildx build \` with `--push` three lines down is one command.
func (s step) command() string { return strings.ReplaceAll(s.Run, "\\\n", " ") }

// attestation is what a step attests: what the steps after it in its job upload (later), or only
// what it uploads itself (self) - a task that attests what it pushes, or a step whose if: is its
// upload's too - and whether it is an attestation that may not happen (unsure).
func (g Gate) attestation(s step) (later, self covers, unsure bool) {
	broad, own := g.attests(s), runsTask(s.command(), g.SelfAttesting)
	switch {
	case !broad.any() && !own:
		return covers{}, covers{}, false
	case s.continues() || (s.If != "" && !g.uploads(s)):
		return covers{}, covers{}, true
	case own:
		return covers{}, covers{true, true}, false
	case s.If == "":
		return broad, covers{}, false
	}
	return covers{}, broad, false
}

// continues is whether a step's failure is passed: an attestation that may fail with the run going
// on is not one.
func (s step) continues() bool {
	return s.ContinueOnError != nil && s.ContinueOnError != false
}

// idToken is whether permissions grant an OIDC token, which keyless signing is.
func idToken(n yaml.Node) bool {
	if n.Kind == yaml.ScalarNode {
		return n.Value == "write-all"
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "id-token" {
			return n.Content[i+1].Value == "write"
		}
	}
	return false
}

// triggers are a workflow's events, however `on:` is spelled.
func triggers(n yaml.Node) []string {
	switch n.Kind {
	case yaml.ScalarNode:
		return []string{n.Value}
	case yaml.SequenceNode:
		var out []string
		for _, c := range n.Content {
			out = append(out, c.Value)
		}
		return out
	case yaml.MappingNode:
		var out []string
		for i := 0; i < len(n.Content); i += 2 {
			out = append(out, n.Content[i].Value)
		}
		return out
	}
	return nil
}

// Check reads every workflow under Root's .github/workflows and fails with each problem found.
func (g Gate) Check() error {
	dir := filepath.Join(g.Root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("attestgate: %w", err)
	}
	var problems []string
	excused := map[string]bool{}
	files := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || (filepath.Ext(name) != ".yml" && filepath.Ext(name) != ".yaml") {
			continue
		}
		files[name] = true
		raw, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // a workflow of the repository under test
		if err != nil {
			return fmt.Errorf("attestgate: %w", err)
		}
		var w workflow
		if err := yaml.Unmarshal(raw, &w); err != nil {
			return fmt.Errorf("attestgate: %s: %w", name, err)
		}
		problems = append(problems, g.check(name, w, excused)...)
	}
	for _, f := range g.MainOnly {
		if !files[f] {
			problems = append(problems, fmt.Sprintf("%s: MainOnly names a workflow there is not", f))
		}
	}
	problems = append(problems, g.checkBuilders(files)...)
	for _, k := range slices.Sorted(maps.Keys(g.NotPublished)) {
		if strings.TrimSpace(g.NotPublished[k]) == "" {
			problems = append(problems, fmt.Sprintf("%s: NotPublished with no reason", k))
		}
		if !excused[k] {
			problems = append(problems, fmt.Sprintf("%s: NotPublished names no upload step: remove it", k))
		}
	}
	if len(problems) > 0 {
		return errors.New("attestgate:\n  " + strings.Join(problems, "\n  "))
	}
	return nil
}

// checkJob is what is wrong with job j at where, as a whole: a release job that may run off main,
// a builder job that may run in another repository, and a call to a builder from a job that may
// run off main or with no token to sign.
func (g Gate) checkJob(where string, j job, mainOnly, builder, token bool) []string {
	var problems []string
	if mainOnly && !mainRef.MatchString(j.If) {
		problems = append(problems, where+": a release job with no if: github.ref == 'refs/heads/main'")
	}
	if builder && !g.ownRepository(j.If) {
		problems = append(problems, where+": a builder job with no if: github.repository == '"+g.Repository+"': another repository's call would sign as the builder")
	}
	if !g.callsBuilder(j) {
		return problems
	}
	if !mainRef.MatchString(j.If) {
		problems = append(problems, where+": calls a builder with no if: github.ref == 'refs/heads/main'")
	}
	// A called workflow has at most its caller's permissions, whatever it declares.
	if !token {
		problems = append(problems, where+": calls the builder with no id-token: write, so it cannot sign")
	}
	return problems
}

func (g Gate) check(file string, w workflow, excused map[string]bool) []string {
	var problems []string
	mainOnly := slices.Contains(g.MainOnly, file)
	if mainOnly && slices.Contains(triggers(w.On), "push") {
		problems = append(problems, fmt.Sprintf("%s: a release workflow with a push trigger: it runs on whatever was pushed", file))
	}
	builder := g.isBuilder(file)
	if on := triggers(w.On); builder && (len(on) != 1 || on[0] != "workflow_call") {
		problems = append(problems, fmt.Sprintf("%s: the builder is on: %v, not workflow_call alone: it runs without a release workflow calling it", file, on))
	}
	for _, jn := range slices.Sorted(maps.Keys(w.Jobs)) {
		j := w.Jobs[jn]
		// A job's permissions replace the workflow's whole, they do not add to them.
		token := idToken(w.Permissions)
		if j.Permissions.Kind != 0 {
			token = idToken(j.Permissions)
		}
		problems = append(problems, g.checkJob(file+"/"+jn, j, mainOnly, builder, token)...)
		attested := covers{}
		for _, s := range j.Steps {
			where := file + "/" + jn + "/" + s.key()
			// A step that attests what it uploads itself - a task that does both - attests under
			// its own if:, which is the upload's too: if it does not run, nothing leaves. It
			// attests nothing for a later step, which may run when it did not.
			later, self, unsure := g.attestation(s)
			if unsure {
				problems = append(problems, fmt.Sprintf("%s: an attestation that may not happen (if: or continue-on-error:)", where))
				continue
			}
			attested = attested.or(later)
			if !g.uploads(s) {
				continue
			}
			if _, ok := g.NotPublished[where]; ok {
				excused[where] = true
				continue
			}
			has := attested.or(self)
			switch {
			case len(g.Builders) > 0 && !builder:
				problems = append(problems, fmt.Sprintf("%s: publishes outside the builders %v: a workflow calls one with uses:", where, g.Builders))
			case !has.provenance:
				problems = append(problems, fmt.Sprintf("%s: publishes with no provenance attested before it in its job", where))
			case g.RequireSBOM && !has.sbom:
				problems = append(problems, fmt.Sprintf("%s: publishes with no SBOM attested before it in its job", where))
			case !token:
				problems = append(problems, fmt.Sprintf("%s: attests with no id-token: write, so it cannot sign", where))
			}
		}
	}
	return problems
}
