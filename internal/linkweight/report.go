package linkweight

import (
	"fmt"
	"io"
	"strings"

	"github.com/spin-stack/go-tools/internal/allow"
)

// Write says the report for each binary where a person reads it: its size, whether the linker
// eliminated unused methods and what stopped it, what it can reach, its heaviest modules, and the
// imports whose cut would drop the most. top bounds the modules and cuts listed.
func Write(w io.Writer, bins []Binary, top int) {
	// A report that cannot be written changes no exit status, which is the answer.
	say := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format+"\n", a...) }
	for i, b := range bins {
		if i > 0 {
			say("")
		}
		say("%s: %s shipped, %d modules, %d packages (%s in no package: strings, funcdata)",
			b.Root, MB(b.Shipped), len(b.Modules), len(b.Packages), MB(b.Unattributed))
		if b.MethodElimination {
			say("  method elimination: on")
		} else {
			say("  method elimination: OFF - every exported method of every reachable type is linked, because of:")
			var kept []Finding
			for _, f := range b.Breakers {
				if f.ViaMethod {
					kept = append(kept, f)
					continue
				}
				say("    %-28s %s", f.Name, strings.Join(f.Chain, " > "))
			}
			if len(kept) > 0 {
				say("    and, linked as a method its type keeps - possibly only because elimination is already off:")
				for _, f := range kept {
					say("    %-28s %s", f.Name, strings.Join(f.Chain, " > "))
				}
			}
		}
		if len(b.Capabilities) == 0 {
			say("  capabilities: none")
		} else {
			say("  capabilities:")
			for _, f := range b.Capabilities {
				note := ""
				if f.ViaMethod && !b.MethodElimination {
					note = "  (through a kept method: may go when elimination is back on)"
				}
				say("    %-28s %s%s", f.Name, strings.Join(f.Chain, " > "), note)
			}
		}
		say("  heaviest modules:")
		for _, m := range b.Modules[:min(top, len(b.Modules))] {
			say("    %9s  %s", MB(m.Bytes), m.Name)
		}
		if len(b.Cuts) > 0 {
			say("  imports whose cut drops the most (package-level estimate):")
			for _, c := range b.Cuts[:min(top, len(b.Cuts))] {
				mods := ""
				if len(c.Modules) > 0 {
					mods = fmt.Sprintf("  (-%d modules)", len(c.Modules))
				}
				say("    %9s  %s > %s%s", MB(c.Bytes), c.From, c.To, mods)
			}
		}
	}
}

// MB is bytes as megabytes to a tenth, the precision a binary's size is talked about in.
func MB(n uint64) string { return fmt.Sprintf("%.1f MB", float64(n)/(1<<20)) }

// Key is a finding as a list line names it: `cmd/boot capability exec`.
func Key(root string, f Finding) string { return root + " " + f.Kind + " " + f.Name }

// Verdict is the gate's answer: the findings no list line explains and the lines that explain
// nothing.
type Verdict struct {
	Unexplained []string
	Stale       []string
}

// OK is whether the gate passes.
func (v Verdict) OK() bool { return len(v.Unexplained) == 0 && len(v.Stale) == 0 }

// Judge holds every binary's capabilities and method-elimination breakers to allowed. Both
// directions fail: a capability nobody wrote down is surface nobody decided on, and a line no
// finding matches means the binary lost something and the list did not.
func Judge(bins []Binary, allowed map[string]bool) Verdict {
	var keys []string
	for _, b := range bins {
		for _, f := range b.Breakers {
			keys = append(keys, Key(b.Root, f))
		}
		for _, f := range b.Capabilities {
			keys = append(keys, Key(b.Root, f))
		}
	}
	unexplained, stale := allow.Split(allowed, keys, func(k string) string { return k })
	return Verdict{Unexplained: unexplained, Stale: stale}
}

// Write says the verdict, and what to do about each line of it.
func (v Verdict) Write(w io.Writer, allowPath string) {
	say := func(a ...any) { _, _ = fmt.Fprintln(w, a...) }
	if len(v.Unexplained) > 0 {
		say()
		say("reached, and no line of " + allowPath + " says why:")
		for _, k := range v.Unexplained {
			say("  " + k)
		}
		say("cut the import that reaches it (the chain above names it), or add the line with the")
		say("reason after '#': a capability is surface somebody decided the binary has.")
	}
	if len(v.Stale) > 0 {
		say()
		say("lines of " + allowPath + " no binary reaches any more - remove them in this commit:")
		for _, k := range v.Stale {
			say("  " + k)
		}
	}
	if v.OK() {
		say()
		say("OK: every capability and every method-elimination breaker has a reason, and every reason still applies.")
	}
}
