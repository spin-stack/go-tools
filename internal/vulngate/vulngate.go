// Package vulngate decides what of a govulncheck scan fails a build: a vulnerability the code
// calls and that has a fix. One the code only imports, or that nobody has fixed, is listed and
// passes: there is nothing to do about the first that lint should demand, and nothing anyone can
// do about the second.
package vulngate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
)

// Finding is one of govulncheck's -format json findings, as far as the gate reads it.
type Finding struct {
	OSV          string  `json:"osv"`
	FixedVersion string  `json:"fixed_version"`
	Trace        []Frame `json:"trace"`
}

// Frame is one frame of a finding's trace, the vulnerable symbol first.
type Frame struct {
	Module   string `json:"module"`
	Version  string `json:"version"`
	Package  string `json:"package"`
	Function string `json:"function"`
	Receiver string `json:"receiver"`
}

// Called is whether the code reaches the vulnerable symbol: govulncheck names a function in the
// first frame only of a symbol-level finding.
func (f Finding) Called() bool { return len(f.Trace) > 0 && f.Trace[0].Function != "" }

// Fails is whether f fails the build.
func (f Finding) Fails() bool { return f.Called() && f.FixedVersion != "" }

// symbol is the vulnerable symbol the code reaches, or the package it imports.
func (f Finding) symbol() string {
	top := f.Trace[0]
	if top.Function == "" {
		return top.Package
	}
	if top.Receiver != "" {
		return top.Package + "." + top.Receiver + "." + top.Function
	}
	return top.Package + "." + top.Function
}

// fix is what fixes f.
func (f Finding) fix() string {
	if f.FixedVersion == "" {
		return "no fix"
	}
	return "fixed in " + f.FixedVersion
}

// Read is the findings of a govulncheck -format json stream: one JSON object per message, the
// findings among them.
func Read(r io.Reader) ([]Finding, error) {
	var out []Finding
	dec := json.NewDecoder(r)
	for {
		var msg struct {
			Finding *Finding `json:"finding"`
		}
		err := dec.Decode(&msg)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("vulngate: reading govulncheck's answer: %w", err)
		}
		if msg.Finding != nil {
			out = append(out, *msg.Finding)
		}
	}
	return out, nil
}

// shown is how many of a vulnerability's symbols its line names: the rest are counted.
const shown = 3

// Judge says, one line per vulnerability in a module sorted by the two, what fails the build and
// what is only listed. govulncheck reports one vulnerability at module, package and symbol level,
// a symbol per call site, and one advisory can name more than one module (the standard library's
// http2 and golang.org/x/net's): a line names the module and version, the symbols the code
// reaches (or the packages it imports) and that module's fix. One that fails is not listed again.
func Judge(findings []Finding) (failing, listed []string) {
	type vuln struct {
		id, module, version, fix string
		symbols                  []string
		fails                    bool
	}
	byID := map[string]*vuln{}
	for _, f := range findings {
		if len(f.Trace) == 0 || f.Trace[0].Package == "" {
			continue // module level: the package and symbol findings say more
		}
		key := f.OSV + " " + f.Trace[0].Module
		v, ok := byID[key]
		if !ok {
			v = &vuln{id: f.OSV, module: f.Trace[0].Module, version: f.Trace[0].Version, fix: f.fix()}
			byID[key] = v
		}
		if f.Fails() && !v.fails {
			v.fails, v.symbols = true, nil
		}
		if f.Fails() == v.fails {
			v.symbols = append(v.symbols, f.symbol())
		}
	}
	for _, key := range slices.Sorted(maps.Keys(byID)) {
		v := byID[key]
		slices.Sort(v.symbols)
		symbols := slices.Compact(v.symbols)
		named := strings.Join(symbols[:min(len(symbols), shown)], ", ")
		if more := len(symbols) - shown; more > 0 {
			named += fmt.Sprintf(" and %d more", more)
		}
		line := fmt.Sprintf("%s: %s %s, %s (%s)", v.id, v.module, v.version, named, v.fix)
		if v.fails {
			failing = append(failing, line)
		} else {
			listed = append(listed, line)
		}
	}
	return failing, listed
}
