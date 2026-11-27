package taskgate

import (
	"path"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/spin-stack/go-tools/internal/shell"
)

// A definition is one task as a repository's Taskfiles write it: where, under which name Task
// knows it, and what it runs.
type definition struct {
	File  string // the Taskfile that writes it
	Line  int
	Local string // its name in that file
	Via   string // the include that names the file, "" for the root Taskfile's own
	// calls are the tasks it runs, by the names Task knows them: a `task:` of its cmds, a dep, and
	// a line of its shell that is Task itself.
	calls []string
	shell []shell.Line
}

// include is the root Taskfile's include of go-tools' common tasks.
type include struct {
	File      string
	Line      int
	Namespace string // the names' prefix: "" when the root includes it with flatten, as it must
	Excludes  bool
	Release   *yaml.Node // the include's RELEASE var, nil when it states none
}

// taskfiles is a repository's tasks read from its files: the root Taskfile and every file it
// includes that the tree has, each task under the name its includes give it.
//
// Read from the YAML, where Tasks asks Task: this is asked of a repository Task may refuse to
// load - two tasks of one name stop every command, the listing among them - and the answer wanted
// is which file wrote the second. So it follows what a file says and no more: an include whose
// path is a template, or that the tree does not have, is not read, and a task named by a
// template is not followed.
type taskfiles struct {
	Root   string // the root Taskfile, "" when the repository has none
	Tasks  map[string]*definition
	Common *include
	// Vars is each var a file of the repository's own states in its top-level vars:, in the
	// order read.
	Vars []ownVar
}

// ownVar is a var a Taskfile states, by its name and where.
type ownVar struct {
	File, Name string
	Line       int
}

// roots are the names Task finds a directory's Taskfile by, in its order.
var roots = []string{"Taskfile.yml", "Taskfile.yaml", "Taskfile.dist.yml", "Taskfile.dist.yaml"}

// readTaskfiles is the repository's tasks, with common the taskfile an include names to have
// go-tools' common tasks.
func readTaskfiles(root string, listed map[string]bool, common string) (*taskfiles, error) {
	t := &taskfiles{Tasks: map[string]*definition{}}
	for _, r := range roots {
		if listed[r] {
			t.Root = r
			break
		}
	}
	if t.Root == "" {
		return t, nil
	}
	return t, t.read(root, listed, common, t.Root, "", "", map[string]bool{})
}

// read adds the tasks of the Taskfile at rel, each named prefix and its own name, and of the
// files it includes. including is the files whose includes led here: a file that includes one of
// them is a cycle, which Task refuses, and is not followed - its prefix would grow without end.
func (t *taskfiles) read(root string, listed map[string]bool, common, rel, prefix, via string, including map[string]bool) error {
	if including[rel] {
		return nil
	}
	including[rel] = true
	defer delete(including, rel)
	raw, err := read(root, rel)
	if err != nil {
		return err
	}
	top, err := document(raw)
	if err != nil || top == nil {
		return err
	}
	t.define(field(top, "tasks"), rel, prefix, via)
	t.state(field(top, "vars"), rel)
	inc := field(top, "includes")
	if inc == nil || inc.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(inc.Content); i += 2 {
		ns, v := inc.Content[i], inc.Content[i+1]
		file, flat := v, false
		if v.Kind == yaml.MappingNode {
			file = field(v, "taskfile")
			if f := field(v, "flatten"); f != nil {
				flat = f.Value == "true"
			}
		}
		if file == nil || file.Kind != yaml.ScalarNode {
			continue
		}
		under := prefix
		if !flat {
			under += ns.Value + ":"
		}
		if strings.Join(strings.Fields(file.Value), "") == common {
			if t.Common == nil {
				t.Common = &include{File: rel, Line: ns.Line, Namespace: under, Excludes: field(v, "excludes") != nil}
				if vars := field(v, "vars"); vars != nil {
					t.Common.Release = field(vars, "RELEASE")
				}
			}
			continue
		}
		p := path.Join(path.Dir(rel), file.Value)
		if !listed[p] {
			p = path.Join(p, "Taskfile.yml")
		}
		if !listed[p] {
			continue
		}
		if err := t.read(root, listed, common, p, under, ns.Value, including); err != nil {
			return err
		}
	}
	return nil
}

// state adds the vars of one file's top-level vars: mapping.
func (t *taskfiles) state(vars *yaml.Node, rel string) {
	if vars == nil || vars.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(vars.Content); i += 2 {
		t.Vars = append(t.Vars, ownVar{rel, vars.Content[i].Value, vars.Content[i].Line})
	}
}

// define adds the tasks of one file's tasks: mapping, each named prefix and its own name.
func (t *taskfiles) define(tasks *yaml.Node, rel, prefix, via string) {
	if tasks == nil || tasks.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(tasks.Content); i += 2 {
		name, body := tasks.Content[i], tasks.Content[i+1]
		d := &definition{File: rel, Line: name.Line, Local: name.Value, Via: via}
		d.calls = calls(body, prefix)
		for _, sh := range collect(name.Value, body) {
			d.shell = append(d.shell, shell.Lines(sh.node.Value, firstLine(sh.node))...)
		}
		for _, l := range d.shell {
			if line := strings.TrimPrefix(l.Text, "{{.TASK_EXE}} "); line != l.Text || isTask(l.Text) {
				d.calls = append(d.calls, taskNames("task "+strings.TrimPrefix(strings.TrimPrefix(line, "go tool "), "task "))...)
			}
		}
		// The first of a name is the one kept: a second is what Task refuses, and Common says.
		if t.Tasks[prefix+name.Value] == nil {
			t.Tasks[prefix+name.Value] = d
		}
	}
}

// calls is the tasks a task's body names, as Task knows them from a file whose tasks are under
// prefix: each `task:` of its cmds and each dep, a name that starts with a colon being the root's.
func calls(n *yaml.Node, prefix string) []string {
	var out []string
	named := func(v *yaml.Node) {
		// A name a template makes is known only to a run. A dep that is a mapping is named by
		// its own task:, read below, and as itself names "", which no task is.
		if strings.Contains(v.Value, "{{") {
			return
		}
		if root, ok := strings.CutPrefix(v.Value, ":"); ok {
			out = append(out, root)
			return
		}
		out = append(out, prefix+v.Value)
	}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, v := n.Content[i].Value, n.Content[i+1]
			switch {
			case key == "task":
				named(v)
			case key == "deps" && v.Kind == yaml.SequenceNode:
				for _, dep := range v.Content {
					named(dep)
					out = append(out, calls(dep, prefix)...)
				}
			default:
				out = append(out, calls(v, prefix)...)
			}
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			out = append(out, calls(item, prefix)...)
		}
	}
	return out
}

// reached is every task the tasks named run, themselves among them, by name: a name no file of
// the repository defines - a common task's - is in it too, and runs nothing the files say.
func (t *taskfiles) reached(names []string) map[string]bool {
	out := map[string]bool{}
	for len(names) > 0 {
		n := names[0]
		names = names[1:]
		if out[n] {
			continue
		}
		out[n] = true
		if d := t.Tasks[n]; d != nil {
			names = append(names, d.calls...)
		}
	}
	return out
}
