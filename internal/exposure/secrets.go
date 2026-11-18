package exposure

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spin-stack/go-tools/internal/allow"
)

// Secrets is gitleaks' JSON report as findings, one for each secret it found, each of kind
// secret:<rule>, its value gitleaks' fingerprint (commit:file:rule:line). A value that is no
// secret is answered in the repository's gitleaks config, where its pre-commit check reads it
// too; this allowlist can name one, but it is the second place to look.
func Secrets(report string, onDefault func(commit string) bool) ([]Finding, error) {
	raw, err := os.ReadFile(report) //nolint:gosec // the report this run had gitleaks write
	if err != nil {
		return nil, err
	}
	var leaks []struct {
		RuleID      string `json:"RuleID"`
		Commit      string `json:"Commit"`
		File        string `json:"File"`
		StartLine   int    `json:"StartLine"`
		Fingerprint string `json:"Fingerprint"`
	}
	if err := json.Unmarshal(raw, &leaks); err != nil {
		return nil, fmt.Errorf("gitleaks' report: %w", err)
	}
	var t tally
	for _, l := range leaks {
		t.add("secret:"+l.RuleID, l.Fingerprint, fmt.Sprintf("commit %s %s:%d", short(l.Commit), l.File, l.StartLine), onDefault(l.Commit))
	}
	return t.findings(), nil
}

// Explain splits findings into those the allowlist does not answer, in order, and the
// allowlist's keys that answer nothing any more, sorted. A key answers a finding by its kind
// and value, or by `<kind> *` for every value of the kind.
func Explain(allowed map[string]bool, found []Finding) (unexplained []Finding, stale []string) {
	return allow.Split(allowed, found, func(f Finding) string {
		if allowed[f.Key()] {
			return f.Key()
		}
		return f.Kind + " *"
	})
}
