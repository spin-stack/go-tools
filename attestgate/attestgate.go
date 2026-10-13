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
	// NotPublished are the steps that look like an upload and ship nothing anyone installs - a
	// CI artifact, a measurement - each "<workflow file>/<job>/<step name or id>" with its
	// reason. One that matches no upload step fails, so the list cannot outlive what it excused.
	NotPublished map[string]string
	// MainOnly are the workflow files that cut a release: none has a push trigger, and every job
	// in one runs only on refs/heads/main.
	MainOnly []string
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

func (g Gate) attests(s step) bool {
	return attestCommand.MatchString(s.command()) || runsTask(s.command(), g.AttestTasks)
}

// command is the step's script with each backslash-continued line joined to the next, as the
// shell reads it: `docker buildx build \` with `--push` three lines down is one command.
func (s step) command() string { return strings.ReplaceAll(s.Run, "\\\n", " ") }

// conditional is whether a step might not run, or might fail and be passed: an attestation that
// may not happen is not one.
func (s step) conditional() bool {
	return s.If != "" || (s.ContinueOnError != nil && s.ContinueOnError != false)
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

func (g Gate) check(file string, w workflow, excused map[string]bool) []string {
	var problems []string
	mainOnly := slices.Contains(g.MainOnly, file)
	if mainOnly && slices.Contains(triggers(w.On), "push") {
		problems = append(problems, fmt.Sprintf("%s: a release workflow with a push trigger: it runs on whatever was pushed", file))
	}
	for _, jn := range slices.Sorted(maps.Keys(w.Jobs)) {
		j := w.Jobs[jn]
		if mainOnly && !mainRef.MatchString(j.If) {
			problems = append(problems, fmt.Sprintf("%s/%s: a release job with no if: github.ref == 'refs/heads/main'", file, jn))
		}
		// A job's permissions replace the workflow's whole, they do not add to them.
		token := idToken(w.Permissions)
		if j.Permissions.Kind != 0 {
			token = idToken(j.Permissions)
		}
		attested := false
		for _, s := range j.Steps {
			where := file + "/" + jn + "/" + s.key()
			if g.attests(s) {
				if s.conditional() {
					problems = append(problems, fmt.Sprintf("%s: an attestation that may not happen (if: or continue-on-error:)", where))
					continue
				}
				attested = true
			}
			if !g.uploads(s) {
				continue
			}
			if _, ok := g.NotPublished[where]; ok {
				excused[where] = true
				continue
			}
			switch {
			case !attested:
				problems = append(problems, fmt.Sprintf("%s: publishes with no provenance attested before it in its job", where))
			case !token:
				problems = append(problems, fmt.Sprintf("%s: attests with no id-token: write, so it cannot sign", where))
			}
		}
	}
	return problems
}
