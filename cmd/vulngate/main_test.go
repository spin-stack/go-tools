package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/spin-stack/go-tools/internal/vulngate"
)

// A scan that did not run, or answered what is not govulncheck's json, is no answer: 2, never a
// clean 0. One that ran is judged; the flags given reach it after -format json's.
func TestAScanItCannotJudgeIsNotClean(t *testing.T) {
	answering := func(answer string, err error) scanner {
		return func(_ context.Context, args []string, stdout, _ io.Writer) error {
			if len(args) != 1 || args[0] != "./..." {
				t.Errorf("the scan was given %v", args)
			}
			_, _ = io.WriteString(stdout, answer)
			return err
		}
	}
	for name, tc := range map[string]struct {
		scan   scanner
		status int
	}{
		"the scan failed":                    {scan: answering("", errors.New("no go.mod")), status: 2},
		"not json":                           {scan: answering("Scanning your code...", nil), status: 2},
		"clean":                              {scan: answering(`{"config":{}}`, nil), status: 0},
		"something the code calls has a fix": {scan: answering(`{"finding":{"osv":"GO-1","fixed_version":"v2","trace":[{"module":"m","package":"m/p","function":"F"}]}}`, nil), status: 1},
	} {
		t.Run(name, func(t *testing.T) {
			var out, errs bytes.Buffer
			assert.Equal(t, tc.status, run(t.Context(), tc.scan, []string{"./..."}, &out, &errs))
		})
	}
}

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
