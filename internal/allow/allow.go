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

// Stale is the keys of allowed that matched does not hold, sorted: an entry that excuses nothing
// is a sentence rewritten or a test fixed, and a list that keeps it stops being a ratchet.
func Stale(allowed, matched map[string]bool) []string {
	var stale []string
	for key := range allowed {
		if !matched[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	return stale
}
