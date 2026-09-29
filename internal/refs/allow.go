package refs

import (
	"fmt"

	"github.com/spin-stack/go-tools/internal/allow"
)

// Allow is the list of references made on purpose to something that is not here - naming a
// thing to say it is gone, or naming something in another repository.
type Allow struct {
	keys map[string]bool
}

// ReadAllow parses `<file><TAB><reference>  # why`, the why required. No path is no allowlist:
// every reference must resolve.
func ReadAllow(path string) (*Allow, error) {
	keys, err := allow.Read(path)
	if err != nil {
		return nil, fmt.Errorf("refs: %w", err)
	}
	return &Allow{keys: keys}, nil
}

// Len is how many exceptions the list holds.
func (a *Allow) Len() int { return len(a.keys) }

// Split turns findings into the two halves the ratchet reports: what nothing explains, and
// what the list explains but no longer happens.
//
// The second half is the direction people forget. An entry that matches nothing means the
// sentence was rewritten or the thing came back, and a list that keeps it goes on excusing a
// reference nobody makes - which is how an allowlist stops being a ratchet and becomes a
// place to put things.
func (a *Allow) Split(found []Finding) (unexplained []Finding, stale []string) {
	matched := map[string]bool{}
	for _, f := range found {
		key := f.Key()
		if a.keys[key] {
			matched[key] = true
			continue
		}
		unexplained = append(unexplained, f)
	}
	return unexplained, allow.Stale(a.keys, matched)
}
