package taskgate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
)

// Tasks is every name the Taskfile in root answers to - each task a workflow may call, by its name
// and its aliases - as task, the Task binary, lists them. Task's own answer rather than a reading
// of the YAML: includes, namespaces, aliases and internal tasks are Task's rules, and a gate that
// re-implemented them would be a second Task to keep in step with the first. The binary the
// caller runs, not whatever is on PATH, so the version that resolves a name is the one a workflow
// runs.
func Tasks(task, root string) (map[string]bool, error) {
	// --no-status: without it Task writes a checksum file for each task with sources, and a gate
	// that writes the tree it reads changes what the next task believes is up to date.
	cmd := exec.Command(task, "--dir", root, "--list-all", "--json", "--no-status") //nolint:gosec // the caller's Task
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("taskgate: %s --list-all in %s: %w: %s", task, root, err, bytes.TrimSpace(stderr.Bytes()))
	}
	return names(out)
}

// names is the names and aliases of Task's --list-all --json, which leaves internal tasks out.
func names(listed []byte) (map[string]bool, error) {
	var l struct {
		Tasks []struct {
			Name    string   `json:"name"`
			Aliases []string `json:"aliases"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(listed, &l); err != nil {
		return nil, fmt.Errorf("taskgate: reading Task's list: %w", err)
	}
	if len(l.Tasks) == 0 {
		// A list with no task is a Task that read no Taskfile, and every step would be Missing
		// for a reason that is not the step's.
		return nil, errors.New("taskgate: Task lists no task")
	}
	out := map[string]bool{}
	for _, t := range l.Tasks {
		out[t.Name] = true
		for _, a := range t.Aliases {
			out[a] = true
		}
	}
	return out, nil
}
