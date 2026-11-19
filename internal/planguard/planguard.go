// Package planguard decides whether an OpenTofu or Terraform plan may be applied without a person
// reading it: what an installation that follows a channel or its module's main applies on a
// schedule, and what it opens a pull request for instead.
//
// A plan is applied unread only when nothing in it is taken away: every change creates a resource
// or updates one in place. A delete, a replacement - delete and create, either order - and a
// forget (state that stops tracking a resource it does not delete) are a person's, whatever the
// resource: the plan cannot say which resources hold data, and a replaced database, bucket or key
// is the kind of change no schedule should make. A resource type a caller names as made to be
// replaced - a launch template's version, an instance an autoscaling group refreshes anyway - may
// be replaced, never deleted alone.
package planguard

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Verdict is what a plan asks for.
type Verdict string

const (
	// Nothing is a plan with no change to a resource: nothing to apply.
	Nothing Verdict = "nothing"
	// Apply is a plan of creates and in-place updates alone, which may be applied unread.
	Apply Verdict = "apply"
	// Person is a plan that takes something away, which a person reads before it is applied.
	Person Verdict = "person"
)

// Decision is a plan's verdict, and each change that made it a person's.
type Decision struct {
	Verdict Verdict
	// Held are the changes a person has to read, "<address>: <actions>", in the plan's order.
	Held []string
	// Changes is how many resources the plan changes.
	Changes int
}

// plan is what `tofu show -json <plan>` says that this reads.
type plan struct {
	FormatVersion   string `json:"format_version"`
	ResourceChanges []struct {
		Address string `json:"address"`
		Type    string `json:"type"`
		Mode    string `json:"mode"`
		Change  struct {
			Actions []string `json:"actions"`
		} `json:"change"`
	} `json:"resource_changes"`
}

// Decide reads a plan as `tofu show -json` writes it. replaceable are the resource types that
// may be replaced unread.
func Decide(raw []byte, replaceable []string) (Decision, error) {
	var p plan
	if err := json.Unmarshal(raw, &p); err != nil {
		return Decision{}, fmt.Errorf("planguard: not a plan's JSON: %w", err)
	}
	// A file with no format is not a plan: an empty object would read as nothing to do.
	if p.FormatVersion == "" {
		return Decision{}, fmt.Errorf("planguard: no format_version: not what `tofu show -json <plan>` writes")
	}
	d := Decision{Verdict: Nothing}
	for _, rc := range p.ResourceChanges {
		actions := rc.Change.Actions
		if len(actions) == 0 {
			return Decision{}, fmt.Errorf("planguard: %s has no actions", rc.Address)
		}
		if changesNothing(actions) {
			continue
		}
		d.Changes++
		if !held(rc.Type, actions, replaceable) {
			continue
		}
		d.Held = append(d.Held, rc.Address+": "+strings.Join(actions, ", "))
	}
	switch {
	case len(d.Held) > 0:
		d.Verdict = Person
	case d.Changes > 0:
		d.Verdict = Apply
	}
	return d, nil
}

// changesNothing is a change that touches no resource: none, or a data source read.
func changesNothing(actions []string) bool {
	return slices.Equal(actions, []string{"no-op"}) || slices.Equal(actions, []string{"read"})
}

// held is a change a person reads: anything but a create or an in-place update - a delete, a
// forget, or an action this does not know - unless it is a replacement of a type made to be
// replaced.
func held(typ string, actions []string, replaceable []string) bool {
	if slices.Equal(actions, []string{"create"}) || slices.Equal(actions, []string{"update"}) {
		return false
	}
	replace := len(actions) == 2 && slices.Contains(actions, "create") && slices.Contains(actions, "delete")
	return !replace || !slices.Contains(replaceable, typ)
}
