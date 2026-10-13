package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/spin-stack/go-tools/internal/vulngate"
)

// The gate's exit status is 1 exactly when something the code calls has a fix, and it says what.
func TestTheExitStatusIsWhatFails(t *testing.T) {
	calledFixed := vulngate.Finding{OSV: "GO-1", FixedVersion: "v2", Trace: []vulngate.Frame{{Module: "m", Version: "v1", Package: "m/p", Function: "F"}}}
	onlyImported := vulngate.Finding{OSV: "GO-2", FixedVersion: "v2", Trace: []vulngate.Frame{{Module: "m", Version: "v1", Package: "m/q"}}}
	for name, tc := range map[string]struct {
		findings []vulngate.Finding
		status   int
		says     string
	}{
		"nothing":            {status: 0, says: "OK"},
		"only imported":      {findings: []vulngate.Finding{onlyImported}, status: 0, says: "not called, or not fixed: GO-2"},
		"called, with a fix": {findings: []vulngate.Finding{onlyImported, calledFixed}, status: 1, says: "called and fixed - upgrade: GO-1"},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			assert.Equal(t, tc.status, report(tc.findings, &out))
			assert.Contains(t, out.String(), tc.says)
		})
	}
}
