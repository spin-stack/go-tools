// Package deadgate judges what a program never reaches against the two lists that say why: the
// allowlist, "unreachable, and that is correct for ever", and the pending list, "unreachable, and
// the deletion is unfinished". Each is a ratchet that turns one way: a finding in neither list
// fails, an entry matching no finding fails, and a symbol both lists claim fails, since it claims
// two opposite things.
//
// A pinned count was rejected: deleting one finding would buy the right to add another.
package deadgate

import (
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
)

// Report is the judgement: what was found that neither list explains, what each list names that
// is no longer found, and what both claim.
type Report struct {
	Found, Allowed, Pending    int
	Unexplained                []string
	StaleAllowed, StalePending []string
	Both                       []string
	// Owed is the pending list whole, sorted: what a green run still owes a deletion.
	Owed []string
}

// Judge holds found, the symbols and packages nothing reaches, to the two lists.
func Judge(found []string, allowed, pending map[string]bool) Report {
	r := Report{Found: len(found), Allowed: len(allowed), Pending: len(pending)}
	seen := map[string]bool{}
	for _, f := range found {
		seen[f] = true
		if !allowed[f] && !pending[f] {
			r.Unexplained = append(r.Unexplained, f)
		}
	}
	for k := range allowed {
		if !seen[k] {
			r.StaleAllowed = append(r.StaleAllowed, k)
		}
		if pending[k] {
			r.Both = append(r.Both, k)
		}
	}
	for k := range pending {
		if !seen[k] {
			r.StalePending = append(r.StalePending, k)
		}
		r.Owed = append(r.Owed, k)
	}
	for _, s := range []*[]string{&r.Unexplained, &r.StaleAllowed, &r.StalePending, &r.Both, &r.Owed} {
		sort.Strings(*s)
	}
	return r
}

// OK is whether nothing fails: every finding explained, every entry still a finding, no symbol
// claimed twice.
func (r Report) OK() bool {
	return len(r.Unexplained) == 0 && len(r.StaleAllowed) == 0 && len(r.StalePending) == 0 && len(r.Both) == 0
}

// Write says the report where a person reads it: the counts, then each failure with what to do
// about it, or the OK and - on a green run, which is the only kind anybody has once this is a
// gate - what the pending list still owes. allowPath and pendingPath name the lists.
func (r Report) Write(w io.Writer, allowPath, pendingPath string) {
	// A report that cannot be written changes no exit status, which is the answer.
	say := func(a ...any) { _, _ = fmt.Fprintln(w, a...) }
	_, _ = fmt.Fprintf(w, "reported: %d    explained by %s: %d    owed a deletion in %s: %d    unexplained: %d\n",
		r.Found, allowPath, r.Allowed, pendingPath, r.Pending, len(r.Unexplained))
	section := func(lines []string, head ...string) {
		if len(lines) == 0 {
			return
		}
		say()
		for _, h := range head {
			say(h)
		}
		for _, l := range lines {
			say("  " + l)
		}
	}
	section(r.Both,
		"these are in BOTH lists, which claims they are permanently fine and must be",
		"deleted at the same time. Keep the line that is true and remove the other:")
	section(r.StaleAllowed,
		"stale entries in "+allowPath+" - deadcode no longer reports these, so they were deleted",
		"or something now calls them. Remove them; an allowlist that outlives its findings",
		"is a hand-written list:")
	section(r.StalePending,
		"these are in "+pendingPath+" and are no longer reported - the deletion happened.",
		"Remove the lines, in the commit that removed the code: the pending list is a",
		"ratchet, and an entry that outlives its finding is how it would stop shrinking:")
	section(r.Unexplained, "no binary reaches these, and nothing says why that is acceptable:")
	if len(r.Unexplained) > 0 {
		say()
		say("each one is a decision, and this step is blocking so that it gets made now:")
		say("  delete it (a component with no caller is a liability),")
		say("  or add it to " + allowPath + " if a test or a lane is its only legitimate caller,")
		say("  or add it to " + pendingPath + " if it is a deletion somebody has to finish.")
		say("Every one of them takes the reason on the same line.")
	}
	if !r.OK() {
		return
	}
	say("OK: every unreachable symbol has a recorded reason, and every reason still applies.")
	if len(r.Owed) == 0 {
		say(pendingPath + " is empty: nothing unreachable is waiting on a decision.")
		return
	}
	section(r.Owed, "still owed a deletion ("+pendingPath+") - the ratchet is not at zero:")
}

// Symbols is what deadcode reported, one `<package path>.<Name>` a line, as the module names
// them: the module's path taken off the front, a symbol in a skipped package dropped - a binary
// that is no root makes its own packages unreachable *from this analysis*, which is a statement
// about the scope and not a finding - sorted, each once. The name and never the file:line: a line
// number changes whenever anything above it does, and a list keyed by position goes stale on
// edits that have nothing to do with it.
func Symbols(out, module string, skipped []string) []string {
	var syms []string
	for line := range strings.SplitSeq(out, "\n") {
		s := strings.TrimSpace(line)
		if s == "" {
			continue
		}
		s = strings.TrimPrefix(s, module+"/")
		if slices.ContainsFunc(skipped, func(p string) bool { return strings.HasPrefix(s, p+"/") }) {
			continue
		}
		syms = append(syms, s)
	}
	return unique(syms)
}

// Unimported is every package of all no root depends on, as `package <path>` lines: coarse on
// purpose - at this granularity the answer is "nothing links this at all", which is a different
// and blunter statement than "this function is unreachable", and the one deadcode cannot make,
// since a package no binary imports is not in the program it reads. A skipped binary and the
// packages under it are outside the gate here as well.
func Unimported(all, deps []string, module string, skipped []string) []string {
	imported := map[string]bool{}
	for _, d := range deps {
		imported[rel(d, module)] = true
	}
	var out []string
	for _, p := range all {
		r := rel(p, module)
		if imported[r] || slices.ContainsFunc(skipped, func(s string) bool { return r == s || strings.HasPrefix(r, s+"/") }) {
			continue
		}
		out = append(out, "package "+r)
	}
	return unique(out)
}

// Roots is every main package of mains that is not skipped, as `./<path>` go takes it: every
// binary the repository ships, since one left out makes everything only it calls look
// unreachable - a longer report, loud rather than silent, and still wrong.
func Roots(mains []string, module string, skipped []string) []string {
	var roots []string
	for _, m := range mains {
		p := "./" + rel(m, module)
		if !slices.Contains(skipped, p) {
			roots = append(roots, p)
		}
	}
	return unique(roots)
}

// rel is an import path relative to the module, "." for the module itself.
func rel(path, module string) string {
	if path == module {
		return "."
	}
	return strings.TrimPrefix(path, module+"/")
}

func unique(s []string) []string {
	sort.Strings(s)
	return slices.Compact(s)
}
