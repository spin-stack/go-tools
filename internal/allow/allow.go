// Package allow is a gate's allowlist: one key a line, each with the reason it is allowed, and the
// keys nothing matches any more. refs and testquality read theirs with it, so an exception needs
// the same why in either.
package allow

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Read parses `<key>  # reason`, the reason required. A blank line and a line that is only a
// comment are neither. No path is no list; a path named and not there is an error, not an empty
// list, since a gate given the wrong path would otherwise excuse nothing and say nothing.
func Read(path string) (map[string]bool, error) {
	allowed := map[string]bool{}
	if path == "" {
		return allowed, nil
	}
	f, err := os.Open(path) //nolint:gosec // the gate's own list, named by the caller
	if err != nil {
		return nil, fmt.Errorf("the allowlist is read on every run: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only

	s := bufio.NewScanner(f)
	for line := 1; s.Scan(); line++ {
		text := strings.TrimSpace(s.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, reason, ok := strings.Cut(text, "#")
		if !ok || strings.TrimSpace(reason) == "" {
			return nil, fmt.Errorf("%s:%d has no reason after '#': %q", path, line, text)
		}
		allowed[strings.TrimSpace(key)] = true
	}
	if err := s.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return allowed, nil
}

// Split turns findings into the two halves the ratchet reports: what nothing in allowed explains,
// in the order found, and the keys of allowed that explain nothing, sorted.
//
// The second half is the direction people forget. An entry that matches nothing means the
// sentence was rewritten or the test fixed, and a list that keeps it goes on excusing something
// nobody does - which is how an allowlist stops being a ratchet and becomes a place to put things.
func Split[T any](allowed map[string]bool, found []T, key func(T) string) (unexplained []T, stale []string) {
	matched := map[string]bool{}
	for _, f := range found {
		k := key(f)
		if allowed[k] {
			matched[k] = true
			continue
		}
		unexplained = append(unexplained, f)
	}
	for k := range allowed {
		if !matched[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	return unexplained, stale
}
