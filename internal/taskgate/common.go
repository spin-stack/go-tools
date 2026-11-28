package taskgate

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
	"golang.org/x/mod/semver"

	"github.com/spin-stack/go-tools/internal/shell"
	common "github.com/spin-stack/go-tools/internal/tasks"
)

// CommonInclude is the taskfile a repository's include names to have go-tools' common tasks: the
// variable `go tool task` names the release's tasks.yml in.
const CommonInclude = "{{.GO_TOOLS_TASKS}}"

const (
	// taskCommand is what a go.mod takes to run `go tool task`.
	taskCommand = "github.com/spin-stack/go-tools/cmd/task"
	// action is what a workflow step uses to put go-tools' task on PATH, outside any go.mod.
	action = "spin-stack/go-tools"
	// install is what puts it there on a laptop.
	install = "go install " + taskCommand + "@latest"
	module  = "github.com/spin-stack/go-tools"
	// firstWithTasks is the first version of the module whose `go tool task` sets GO_TOOLS_TASKS.
	firstWithTasks = "v0.20261122.4"
	// lintWorkflows is the common task that reads the workflows: the one a repository's CI has to reach.
	lintWorkflows = "lint:workflows"
)

// Uncommon is one way a repository does not have go-tools' common tasks as every other has them.
// At is where, a file and its line when there is one, and Says what is wrong and what to write.
type Uncommon struct {
	Rule string // what an allowlist names it by, after `common:`
	At   string
	Says string
}

// Key is common:<rule>.
func (u Uncommon) Key() string { return "common:" + u.Rule }

func (u Uncommon) String() string { return u.At + ": " + u.Says }

// Common is how the repository under root does not have go-tools' common tasks - versions, bump,
// release, lint:workflows, consumers - as every other repository has them, and a line that says
// how it does. included is the taskfile an include names to have them: CommonInclude, and in
// go-tools itself the tree's own file.
//
// It holds a repository that runs go-tools' task - its workflows use the action, or its go.mod
// takes the command - or includes the tasks; of any other it asks nothing, and says so. All of it
// is read from the tree: nothing is run, Task least of all, since a repository with a second task
// of a common name is one Task stops at.
func Common(ctx context.Context, root, included string) ([]Finding, string, error) {
	listed, workflows, err := list(ctx, root)
	if err != nil {
		return nil, "", err
	}
	gomod, err := read(root, "go.mod")
	if err != nil {
		return nil, "", err
	}
	tool := slices.Contains(strings.Fields(string(gomod)), taskCommand)
	t, err := readTaskfiles(root, listed, included)
	if err != nil {
		return nil, "", err
	}
	flows, err := readFlows(root, workflows)
	if err != nil {
		return nil, "", err
	}
	installs := installers(flows)
	installed := slices.ContainsFunc(flows, func(f flow) bool { return installs(f.File) })
	pins := listed["versions.yaml"]
	if !tool && !installed && !pins && t.Common == nil {
		return nil, "the common tasks: not asked of this repository, whose workflows do not use " + action + ", whose go.mod does not take " + taskCommand +
			", which has no versions.yaml, and whose Taskfile does not include them", nil
	}

	var out []Uncommon
	if tool {
		out = append(out, requirement(string(gomod))...)
	}
	if tool || installed {
		out = append(out, bare(flows, installs, tool)...)
	}
	out = append(out, own(t)...)
	out = append(out, hidden(t)...)
	said := "no include"
	if t.Common == nil {
		file := cmp.Or(t.Root, roots[0])
		out = append(out, Uncommon{"include", file, asked(tool, installed) + ", and this Taskfile does not include go-tools' common tasks. " +
			"Add to its includes `common: {taskfile: \"" + CommonInclude + "\", flatten: true, vars: {RELEASE: {map: {none: <why it does not release>}}}}`, " +
			"with RELEASE as the repository is released, and delete or rename its own tasks of those names"})
	} else {
		at := t.Common.File + ":" + strconv.Itoa(t.Common.Line)
		if t.Common.File != t.Root || t.Common.Namespace != "" {
			out = append(out, Uncommon{"include", at, "go-tools' common tasks are included under the names " + t.Common.Namespace + "<task>, from " + t.Common.File +
				": the include is the root Taskfile's, with `flatten: true`, so that `versions` is `versions` in every repository"})
		}
		if t.Common.Excludes {
			out = append(out, Uncommon{"excludes", at, "the include of go-tools' common tasks has `excludes`, which lets a task of the repository's own answer to a common name: " +
				"remove `excludes`, and rename the repository's task after what it does"})
		}
		var findings []Uncommon
		findings, said = release(t.Common, at, listed, flows, t)
		out = append(out, findings...)
		out = append(out, unreached(t, flows, at)...)
		out = append(out, labels(root, listed, flows)...)
	}
	found := make([]Finding, len(out))
	for i, u := range out {
		found[i] = u
	}
	return found, "the common tasks: included once and flattened, none of their names the repository's own, RELEASE says " + said +
		", and " + lintWorkflows + " is in what a workflow runs", nil
}

// asked is why a repository is asked for the include: it runs go-tools' task, or it pins.
func asked(tool, installed bool) string {
	switch {
	case installed:
		return "its workflows use " + action
	case tool:
		return "go.mod takes " + taskCommand
	}
	return "versions.yaml pins, and `task versions` is how every repository says what is behind, with go-tools' task: " + action + " in a workflow, `" + install + "` on a laptop"
}

// readFlows is each of workflows, the workflows and composite actions under root, read.
func readFlows(root string, workflows []string) ([]flow, error) {
	var flows []flow
	for _, rel := range workflows {
		raw, err := read(root, rel)
		if err != nil {
			return nil, err
		}
		top, err := document(raw)
		if err != nil {
			return nil, fmt.Errorf("taskgate: %s: %w", rel, err)
		}
		if top != nil {
			flows = append(flows, readFlow(rel, top))
		}
	}
	return flows, nil
}

// list is what git lists under root, tracked or untracked and not ignored, and the workflows and
// composite actions among it.
func list(ctx context.Context, root string) (listed map[string]bool, workflows []string, err error) {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		return nil, nil, fmt.Errorf("taskgate: listing %s: %w", root, err)
	}
	listed = map[string]bool{}
	for rel := range strings.FieldsSeq(string(out)) {
		listed[rel] = true
		if workflow(rel) {
			workflows = append(workflows, rel)
		}
	}
	return listed, workflows, nil
}

// required is the version of this module a go.mod requires, in a require block or on a line of its own.
var required = regexp.MustCompile(`(?m)^\s*(?:require\s+)?` + regexp.QuoteMeta(module) + `\s+(v\S+)`)

// requirement is a go.mod whose go-tools is from before its `go tool task` named the tasks.
func requirement(gomod string) []Uncommon {
	m := required.FindStringSubmatch(gomod)
	if m == nil || semver.Compare(m[1], firstWithTasks) >= 0 {
		return nil
	}
	return []Uncommon{{"go-tools", "go.mod", "requires " + module + " " + m[1] + ", from before " + firstWithTasks + ", the first whose `go tool task` sets GO_TOOLS_TASKS: " +
		"with it an include of the common tasks is `include cycle detected` for every task. `go get " + module + "@latest && go mod tidy`"}}
}

// commonNames is the common tasks' names, from the tasks' own file at the version this gate is of.
func commonNames() []string {
	var file struct {
		Tasks map[string]yaml.Node `yaml:"tasks"`
	}
	_ = yaml.Unmarshal(common.File, &file) //nolint:errcheck // this module's own file, which its tests run under Task
	return slices.Sorted(maps.Keys(file.Tasks))
}

// own is each common name a file of the repository defines: in the root Taskfile, or in a file an
// include gives a namespace, where `workflows` under `lint` is `lint:workflows`. Task's error for
// either names the include and not the file, and is every task's.
func own(t *taskfiles) []Uncommon {
	var out []Uncommon
	for _, name := range commonNames() {
		d := t.Tasks[name]
		if d == nil {
			continue
		}
		u := Uncommon{Rule: d.File + ":" + d.Local, At: d.File + ":" + strconv.Itoa(d.Line)}
		if d.Via == "" {
			u.Says = "task " + name + " is one of go-tools' common tasks, which the include defines: delete it, or rename it after what it does if it does something else " +
				"(with both Task refuses every task: `Found multiple tasks (" + name + ")`)"
		} else {
			u.Says = "task " + d.Local + " is " + name + " through the include `" + d.Via + "`, and " + name + " is one of go-tools' common tasks: rename it in " + d.File +
				", after what it does (with both Task refuses every task: `Found multiple tasks (" + name + ") included by \"" + d.Via + "\"\"`)"
		}
		out = append(out, u)
	}
	return out
}

// templated is a template of Task's, and dotted a var it reads: `.RELEASE` of `.RELEASE.none`,
// and not a word of a string the template holds, which is not upper case after a dot.
var (
	templated = regexp.MustCompile(`\{\{[^}]*\}\}`)
	dotted    = regexp.MustCompile(`\.([A-Z][A-Z0-9_]*)`)
)

// taskSets are the vars Task gives every task itself. A template of the common tasks may read
// one, and no repository states one, so they are not names a repository is told to rename.
var taskSets = []string{
	"ALIAS", "CHECKSUM", "CLI_ARGS", "CLI_ARGS_LIST", "CLI_ASSUME_YES", "CLI_FORCE", "CLI_OFFLINE", "CLI_SILENT", "CLI_VERBOSE",
	"EXIT_CODE", "ITEM", "MATCH", "ROOT_DIR", "ROOT_TASKFILE", "TASK", "TASKFILE", "TASKFILE_DIR", "TASK_DIR", "TASK_EXE",
	"TASK_VERSION", "TIMESTAMP", "USER_WORKING_DIR",
}

// commonVars is the vars the common tasks' templates read and do not state themselves, from the
// tasks' own file at the version this gate is of: the include's (RELEASE) and the command line's
// (TO). A var a task requires is one its templates read, or requiring it would be for nothing.
// A task's own vars are read after every other, so a repository's of the same name hides nothing.
func commonVars() []string {
	var top yaml.Node
	_ = yaml.Unmarshal(common.File, &top) //nolint:errcheck // this module's own file, which its tests run under Task
	read := map[string]bool{}
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		for _, tmpl := range templated.FindAllString(n.Value, -1) { // mutate-exempt: any n < 0 is every match
			for _, m := range dotted.FindAllStringSubmatch(tmpl, -1) { // mutate-exempt: any n < 0 is every match
				read[m[1]] = true
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(&top)
	var file struct {
		Tasks map[string]struct {
			Vars map[string]yaml.Node `yaml:"vars"`
		} `yaml:"tasks"`
	}
	_ = top.Decode(&file) //nolint:errcheck // this module's own file, which its tests run under Task
	for _, task := range file.Tasks {
		for n := range task.Vars {
			delete(read, n)
		}
	}
	for _, n := range taskSets {
		delete(read, n)
	}
	return slices.Sorted(maps.Keys(read))
}

// hidden is each var a Taskfile of the repository states under a name the common tasks read: Task
// gives it to an included task over the include's vars and the command line's, so the task reads
// the repository's value - a root RELEASE that is a directory is a release the task says nobody
// stated, while the include states one.
func hidden(t *taskfiles) []Uncommon {
	names := commonVars()
	var out []Uncommon
	for _, v := range t.Vars {
		if !slices.Contains(names, v.Name) {
			continue
		}
		out = append(out, Uncommon{v.File + ":vars:" + v.Name, v.File + ":" + strconv.Itoa(v.Line), "var " + v.Name + " is one the common tasks read, " +
			"and a Taskfile's own var of that name is what they read instead of the include's or the command line's: rename it after what it holds"})
	}
	return out
}

// A flow is a workflow, or a composite action as one job with no id.
type flow struct {
	File string
	// Tags is a workflow started by a tag pushed or a release published.
	Tags bool
	Jobs []job
}

type job struct {
	ID, Name string
	RunsOn   []label
	Steps    []step
}

type label struct {
	Value string
	Line  int
}

type step struct {
	Name string // as Step names it: its name, its id, or #n
	Line int
	Uses string
	Run  []shell.Line
}

// readFlow is the workflow or composite action in top.
func readFlow(rel string, top *yaml.Node) flow {
	f := flow{File: rel}
	// `on` is YAML 1.1's true to some writers, and quoted for it.
	switch on := field(top, "on"); {
	case on == nil:
	case on.Kind == yaml.MappingNode:
		if push := field(on, "push"); push != nil && field(push, "tags") != nil {
			f.Tags = true
		}
		f.Tags = f.Tags || field(on, "release") != nil
	case on.Kind == yaml.ScalarNode:
		f.Tags = on.Value == "release"
	case on.Kind == yaml.SequenceNode:
		f.Tags = slices.ContainsFunc(on.Content, func(e *yaml.Node) bool { return e.Value == "release" })
	}
	if runs := field(top, "runs"); runs != nil {
		f.Jobs = append(f.Jobs, job{Steps: readSteps(field(runs, "steps"))})
	}
	if jobs := field(top, "jobs"); jobs != nil && jobs.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(jobs.Content); i += 2 {
			id, body := jobs.Content[i].Value, jobs.Content[i+1]
			j := job{ID: id, Name: id, Steps: readSteps(field(body, "steps"))}
			if n := field(body, "name"); n != nil && n.Value != "" {
				j.Name = n.Value
			}
			if on := field(body, "runs-on"); on != nil {
				if on.Kind == yaml.MappingNode {
					on = field(on, "labels")
				}
				switch {
				case on == nil:
				case on.Kind == yaml.ScalarNode:
					j.RunsOn = append(j.RunsOn, label{on.Value, on.Line})
				case on.Kind == yaml.SequenceNode:
					for _, l := range on.Content {
						j.RunsOn = append(j.RunsOn, label{l.Value, l.Line})
					}
				}
			}
			f.Jobs = append(f.Jobs, j)
		}
	}
	return f
}

func readSteps(steps *yaml.Node) []step {
	if steps == nil || steps.Kind != yaml.SequenceNode {
		return nil
	}
	var out []step
	for i, n := range steps.Content {
		s := step{Name: "#" + strconv.Itoa(i+1), Line: n.Line}
		if id := field(n, "id"); id != nil {
			s.Name = id.Value
		}
		if name := field(n, "name"); name != nil {
			s.Name = name.Value
		}
		if uses := field(n, "uses"); uses != nil {
			s.Uses = uses.Value
		}
		if run := field(n, "run"); run != nil {
			s.Run = shell.Lines(run.Value, firstLine(run))
		}
		out = append(out, s)
	}
	return out
}

// key is <file>/<job>/<step>, as Step's.
func (f flow) key(j job, s step) string {
	if j.ID == "" {
		return f.File + "/" + s.Name
	}
	return f.File + "/" + j.ID + "/" + s.Name
}

// bare is each step of a repository that runs go-tools' task which runs Task another way: a line
// that starts with `task` with nothing before it in its job that installs go-tools' task, which is
// whatever Task the runner has and knows no GO_TOOLS_TASKS, and the action that installs one.
// tool is a go.mod that takes the command, where `go tool task` is the other way to write it.
func bare(flows []flow, installs func(file string) bool, tool bool) []Uncommon {
	var out []Uncommon
	fix := "add a step before it that uses " + action + " (`uses: " + action + "@<commit>`), which puts go-tools' task on PATH"
	if tool {
		fix = "write `go tool <line>`, or " + fix
	}
	for _, f := range flows {
		for _, j := range f.Jobs {
			installed := false
			for _, s := range j.Steps {
				installed = installed || installer(s.Uses, f.File, installs)
				if strings.HasPrefix(s.Uses, "go-task/setup-task@") || s.Uses == "go-task/setup-task" {
					out = append(out, Uncommon{f.key(j, s) + "/setup-task", f.File + ":" + strconv.Itoa(s.Line),
						"step " + strconv.Quote(s.Name) + " installs Task with go-task/setup-task: remove the step, " + action +
							" installs the Task go-tools pins with go-tools' commands on its PATH"})
				}
				for _, l := range s.Run {
					if !installed && strings.HasPrefix(l.Text+" ", "task ") {
						out = append(out, Uncommon{f.key(j, s) + "/task", f.File + ":" + strconv.Itoa(l.Line),
							"step " + strconv.Quote(s.Name) + " runs `" + l.Text + "` with nothing before it that installs go-tools' task: " +
								strings.ReplaceAll(fix, "<line>", l.Text) + ". A bare `task` is whatever Task the runner has, with no GO_TOOLS_TASKS, and stops at `include cycle detected`"})
					}
				}
			}
		}
	}
	return out
}

// installers says of a workflow or composite action, by its file, whether one of its steps puts
// go-tools' task on PATH: it uses the action, or a composite action of the repository that does.
func installers(flows []flow) func(file string) bool {
	byFile := map[string]flow{}
	for _, f := range flows {
		byFile[f.File] = f
	}
	known := map[string]bool{}
	var installs func(file string) bool
	installs = func(file string) bool {
		if v, ok := known[file]; ok {
			return v
		}
		known[file] = false // a composite that uses itself installs nothing by it
		for _, j := range byFile[file].Jobs {
			for _, s := range j.Steps {
				if installer(s.Uses, file, installs) {
					known[file] = true
					return true
				}
			}
		}
		return false
	}
	return installs
}

// installer is whether a step that uses uses, in file, puts go-tools' task on PATH.
func installer(uses, file string, installs func(file string) bool) bool {
	if strings.HasPrefix(uses, action+"@") {
		return true
	}
	local, ok := strings.CutPrefix(uses, "./")
	if !ok {
		return false
	}
	for _, name := range []string{"action.yml", "action.yaml"} {
		if composite := path.Join(local, name); composite != file && installs(composite) {
			return true
		}
	}
	return false
}

// ran is the tasks the workflows' steps run, by name: every line of a step that is Task.
func ran(flows []flow) []string {
	var out []string
	for _, f := range flows {
		for _, j := range f.Jobs {
			for _, s := range j.Steps {
				for _, l := range s.Run {
					if isTask(l.Text) {
						out = append(out, taskNames(l.Text)...)
					}
				}
			}
		}
	}
	return out
}

// unreached is a repository with workflows none of which runs lint:workflows, by a step or by a
// task a step's task runs: nothing runs a common task unless the repository's own do, and this is
// the one that reads the workflows themselves.
func unreached(t *taskfiles, flows []flow, at string) []Uncommon {
	if len(flows) == 0 || t.reached(ran(flows))[lintWorkflows] {
		return nil
	}
	return []Uncommon{{lintWorkflows, at, lintWorkflows + " is run by no task a workflow runs, so nothing holds the workflows to actionlint and to actions pinned by commit: " +
		"add `- task: " + lintWorkflows + "` to the cmds of the task CI runs (lint, check or gate) in " + t.Root}}
}

// hosted is a label GitHub's own runners have, or one of the labels every self-hosted runner has,
// which actionlint knows without being told.
var hosted = regexp.MustCompile(`^(?:(?:ubuntu|windows|macos)-[a-z0-9.-]+|self-hosted|linux|windows|macos|x64|x86|arm|arm64|gpu)$`)

// labels is each label a job runs on that is not GitHub's and that .github/actionlint.yaml does
// not name: lint:workflows fails on it (`label "..." is unknown`). A label an expression makes is
// only known to a run, and is not read.
func labels(root string, listed map[string]bool, flows []flow) []Uncommon {
	var config struct {
		Runner struct {
			Labels []string `yaml:"labels"`
		} `yaml:"self-hosted-runner"`
	}
	file := ".github/actionlint.yaml"
	if !listed[file] && listed[".github/actionlint.yml"] {
		file = ".github/actionlint.yml"
	}
	if raw, err := read(root, file); err == nil {
		_ = yaml.Unmarshal(raw, &config) //nolint:errcheck // a file actionlint refuses is lint:workflows' to say
	}
	var out []Uncommon
	seen := map[string]bool{}
	for _, f := range flows {
		for _, j := range f.Jobs {
			for _, l := range j.RunsOn {
				if seen[l.Value] || strings.Contains(l.Value, "${{") || hosted.MatchString(l.Value) ||
					slices.ContainsFunc(config.Runner.Labels, func(p string) bool { ok, _ := path.Match(p, l.Value); return ok }) {
					continue
				}
				seen[l.Value] = true
				out = append(out, Uncommon{"label:" + l.Value, f.File + ":" + strconv.Itoa(l.Line), "job " + j.ID + " runs on " + strconv.Quote(l.Value) +
					", which is not one of GitHub's labels and " + file + " does not name: " + lintWorkflows + " fails on it. Write in " + file +
					" `self-hosted-runner: {labels: [" + l.Value + "]}`, with a comment that says whose runners those are"})
			}
		}
	}
	return out
}

// releases are the things a line of shell does that make a release: a tag made or pushed, a
// GitHub release created, GoReleaser's release.
var releases = regexp.MustCompile(`\bgh release create\b|\bgit tag\s+(?:-[asfm]\b|[^-\s])|\bgit push\b.*(?:--tags|--follow-tags|refs/tags/)|\bgh api\b.*\bgit/refs\b|\bgoreleaser release\b`)

// releaseActions are the actions that publish a release.
var releaseActions = []string{"softprops/action-gh-release", "ncipollo/release-action", "actions/create-release", "goreleaser/goreleaser-action"}

// releasing is why the workflow f is one that releases the repository, "" when nothing in the
// files says it is: its name, what starts it, an action it uses, or what its steps and the tasks
// they run say in their own shell.
//
// That is what the files can say. A release made by a program a step runs - a command of the
// repository's own, a script - is not read, and a repository whose only release is one of those
// can state `none` and pass: the statement is checked against the evidence there is, not proven.
func releasing(f flow, t *taskfiles) string {
	if base := strings.TrimSuffix(path.Base(f.File), path.Ext(f.File)); base == "release" && strings.HasPrefix(f.File, ".github/workflows/") {
		return "it is named " + path.Base(f.File)
	}
	if f.Tags {
		return "a tag pushed or a release published starts it"
	}
	for _, j := range f.Jobs {
		for _, s := range j.Steps {
			for _, a := range releaseActions {
				if strings.HasPrefix(s.Uses, a+"@") {
					return "step " + strconv.Quote(s.Name) + " uses " + a
				}
			}
			for _, l := range s.Run {
				if m := releases.FindString(l.Text); m != "" {
					return "step " + strconv.Quote(s.Name) + " runs `" + strings.TrimSpace(m) + "`"
				}
			}
		}
	}
	reached := t.reached(ran([]flow{f}))
	for _, name := range slices.Sorted(maps.Keys(reached)) {
		d := t.Tasks[name]
		if d == nil {
			continue
		}
		for _, l := range d.shell {
			if m := releases.FindString(l.Text); m != "" {
				return "it runs task " + name + ", which runs `" + strings.TrimSpace(m) + "` (" + d.File + ":" + strconv.Itoa(l.Line) + ")"
			}
		}
	}
	return ""
}

// release is what the include's RELEASE says against the repository's own files, and the
// statement in a few words. The task refuses on what the map says; this is whether the map is
// true: a repository that says it does not release and has a workflow that does, a proof that
// names a workflow or a job the repository does not have - which the task finds only on the day
// of a release - and a release said to be made by a file that is not there.
func release(inc *include, at string, listed map[string]bool, flows []flow, t *taskfiles) ([]Uncommon, string) {
	state := map[string]string{}
	var unknown []Uncommon
	if m := inc.Release; m != nil && m.Kind == yaml.MappingNode {
		if m = field(m, "map"); m != nil && m.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(m.Content); i += 2 {
				k, v := m.Content[i], m.Content[i+1]
				state[k.Value] = strings.TrimSpace(v.Value)
				if u, ok := releaseKey(inc.File, k, v); !ok {
					unknown = append(unknown, u)
				}
			}
		}
	}
	found, said := stated(inc, at, state, listed, flows, t)
	return append(unknown, found...), said
}

// releaseKeys are the keys of RELEASE the release task reads.
var releaseKeys = []string{"proof", "jobs", "files", "elsewhere", "none"}

// releaseKey is a key of RELEASE's map that the release task reads and that has a value, or what
// is wrong with it. Either is a statement that says less than its author meant, which neither the
// task nor the rest of the gate notices: a misspelt key is read by nothing, and in a flow map an
// unquoted comma ends the value, the rest of the sentence becoming a key with no value.
func releaseKey(file string, k, v *yaml.Node) (Uncommon, bool) {
	known := slices.Contains(releaseKeys, k.Value)
	empty := v.Kind == yaml.ScalarNode && strings.TrimSpace(v.Value) == ""
	if known && !empty {
		return Uncommon{}, true
	}
	var what string
	switch {
	case !known && empty:
		what = "is not one of " + strings.Join(releaseKeys, ", ") + " and has no value"
	case !known:
		what = "is not one of " + strings.Join(releaseKeys, ", ")
	default:
		what = "has no value"
	}
	return Uncommon{"RELEASE/key/" + k.Value, file + ":" + strconv.Itoa(k.Line), "RELEASE's key " + strconv.Quote(k.Value) + " " + what +
		", so it says nothing the release task reads. A value that holds a comma is quoted (`{none: \"a, b\"}`): unquoted, the comma ends it and the rest is a key of its own"}, false
}

// stated is release's reading of the map's known keys.
func stated(inc *include, at string, state map[string]string, listed map[string]bool, flows []flow, t *taskfiles) ([]Uncommon, string) {
	const forms = "`RELEASE: {map: {proof: <workflow>, jobs: <jobs>}}` where `go tool task release` cuts the releases, " +
		"`{map: {elsewhere: <workflow>}}` where a workflow of the repository's own does, `{map: {none: <why>}}` where it does not release"
	switch {
	case state["elsewhere"] != "":
		var out []Uncommon
		for file := range strings.FieldsSeq(state["elsewhere"]) {
			if !listed[".github/workflows/"+file] {
				out = append(out, Uncommon{"RELEASE/elsewhere/" + file, at, "RELEASE's elsewhere names " + file + ", and there is no .github/workflows/" + file +
					": it is the file name of the workflow that releases the repository, as release.yml"})
			}
		}
		return out, "elsewhere: " + state["elsewhere"]
	case state["none"] != "":
		var out []Uncommon
		for _, f := range flows {
			if why := releasing(f, t); why != "" {
				out = append(out, Uncommon{"RELEASE/none/" + path.Base(f.File), f.File, "releases the repository (" + why + "), and RELEASE in " + inc.File +
					" says none: state `RELEASE: {map: {elsewhere: " + path.Base(f.File) + "}}` in the include's vars"})
			}
		}
		return out, "none"
	case state["proof"] != "" && state["jobs"] != "":
		said := "proof: " + state["proof"] + ", jobs: " + state["jobs"]
		i := slices.IndexFunc(flows, func(f flow) bool { return f.File == ".github/workflows/"+state["proof"] })
		if i < 0 {
			return []Uncommon{{"RELEASE/proof", at, "RELEASE's proof names " + state["proof"] + ", and there is no .github/workflows/" + state["proof"] +
				": it is the file name of the workflow whose pass proves a commit can be released"}}, said
		}
		var out []Uncommon
		for want := range strings.SplitSeq(state["jobs"], ",") {
			if want = strings.TrimSpace(want); want != "" && !slices.ContainsFunc(flows[i].Jobs, func(j job) bool { return named(j.Name, want) }) {
				var have []string
				for _, j := range flows[i].Jobs {
					have = append(have, strconv.Quote(j.Name))
				}
				out = append(out, Uncommon{"RELEASE/jobs/" + want, at, "RELEASE's jobs names " + strconv.Quote(want) + ", and no job of " + flows[i].File +
					" has a name that starts with it, so no run proves a release: its jobs are " + strings.Join(have, ", ") + " (a job is named by its `name:`, or its key when it has none)"})
			}
		}
		return out, said
	case len(state) > 0:
		return []Uncommon{{"RELEASE", at, "RELEASE has neither `elsewhere`, `none`, nor both `proof` and `jobs`, so `release` refuses and nothing says why. One of " + forms}}, "nothing"
	}
	return []Uncommon{{"RELEASE", at, "the include of go-tools' common tasks states no RELEASE, a map of its vars that says how the repository is released: " + forms}}, "nothing"
}

// named is whether a job's name starts with want, as proven asks it of a run's jobs. A name an
// expression fills in - a matrix's - is known up to the expression, and what is after it could
// be anything: it is taken as a match, since only a run says.
func named(name, want string) bool {
	if static, _, dynamic := strings.Cut(name, "${{"); dynamic {
		return strings.HasPrefix(static, want) || strings.HasPrefix(want, static)
	}
	return strings.HasPrefix(name, want)
}
