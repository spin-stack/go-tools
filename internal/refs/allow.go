package refs

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Allow is the list of references made on purpose to something that is not here - naming a
// thing to say it is gone, or naming something in another repository.
type Allow struct {
	keys map[string]bool
}

// ReadAllow parses `<file><TAB><reference>  # why`. A blank line and a line that is only a
// comment are neither. No path is no allowlist: every reference must resolve.
func ReadAllow(path string) (*Allow, error) {
	if path == "" {
		return &Allow{keys: map[string]bool{}}, nil
	}
	f, err := os.Open(path) //nolint:gosec // the path is the gate's own list, named by the caller
	if err != nil {
		return nil, fmt.Errorf("refs: the allowlist is read on every run: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only

	a := &Allow{keys: map[string]bool{}}
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		a.keys[line] = true
	}
	if err := s.Err(); err != nil {
		return nil, fmt.Errorf("refs: reading %s: %w", path, err)
	}
	return a, nil
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
	for key := range a.keys {
		if !matched[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	return unexplained, stale
}
