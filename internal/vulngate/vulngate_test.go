package vulngate_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/internal/vulngate"
)

// govulncheck reports one vulnerability three times - at its module, its package and, when the
// code calls it, its symbol - with the progress and the OSV records around them.
const (
	config   = `{"config":{"protocol_version":"v1.0.0","scanner_name":"govulncheck"}}`
	progress = `{"progress":{"message":"Scanning your code..."}}`
	osv      = `{"osv":{"id":"GO-2026-0001","summary":"html: quadratic parse"}}`
	module   = `{"finding":{"osv":"GO-2026-0001","fixed_version":"v0.30.0","trace":[{"module":"golang.org/x/net","version":"v0.29.0"}]}}`
	pkg      = `{"finding":{"osv":"GO-2026-0001","fixed_version":"v0.30.0","trace":[{"module":"golang.org/x/net","version":"v0.29.0","package":"golang.org/x/net/html"}]}}`
	called   = `{"finding":{"osv":"GO-2026-0001","fixed_version":"v0.30.0","trace":[{"module":"golang.org/x/net","version":"v0.29.0","package":"golang.org/x/net/html","function":"Parse"},{"module":"example.com/app","package":"example.com/app","function":"main"}]}}`
	unfixed  = `{"finding":{"osv":"GO-2026-0002","trace":[{"module":"example.com/lib","version":"v1.0.0","package":"example.com/lib","function":"Do"}]}}`
	stdlib   = `{"finding":{"osv":"GO-2026-0003","fixed_version":"go1.27.3","trace":[{"module":"stdlib","version":"go1.27.2","package":"net/http","function":"Serve","receiver":"*Server"}]}}`
)

// What fails is a vulnerability the code calls and that has a fix, the standard library's
// included; one only imported, or with no fix, is listed and passes.
func TestAFixedVulnerabilityTheCodeCallsFails(t *testing.T) {
	for name, tc := range map[string]struct {
		stream  []string
		failing []string
		listed  []string
	}{
		"nothing found": {stream: []string{config, progress}},
		"imported, not called": {stream: []string{config, osv, module, pkg},
			listed: []string{"GO-2026-0001: golang.org/x/net v0.29.0, golang.org/x/net/html (fixed in v0.30.0)"}},
		"called, with a fix": {stream: []string{config, osv, module, pkg, called},
			failing: []string{"GO-2026-0001: golang.org/x/net v0.29.0, golang.org/x/net/html.Parse (fixed in v0.30.0)"}},
		"called from two places, said once": {stream: []string{config, called, called, stdlib},
			failing: []string{
				"GO-2026-0001: golang.org/x/net v0.29.0, golang.org/x/net/html.Parse (fixed in v0.30.0)",
				"GO-2026-0003: stdlib go1.27.2, net/http.*Server.Serve (fixed in go1.27.3)",
			}},
		"called at many symbols, three named": {stream: []string{
			strings.ReplaceAll(called, "Parse", "A"), strings.ReplaceAll(called, "Parse", "B"),
			strings.ReplaceAll(called, "Parse", "C"), strings.ReplaceAll(called, "Parse", "D"),
		}, failing: []string{"GO-2026-0001: golang.org/x/net v0.29.0, golang.org/x/net/html.A, golang.org/x/net/html.B, golang.org/x/net/html.C and 1 more (fixed in v0.30.0)"}},
		"one advisory in two modules, a line each": {stream: []string{
			strings.ReplaceAll(stdlib, "GO-2026-0003", "GO-2026-0001"), called,
		}, failing: []string{
			"GO-2026-0001: golang.org/x/net v0.29.0, golang.org/x/net/html.Parse (fixed in v0.30.0)",
			"GO-2026-0001: stdlib go1.27.2, net/http.*Server.Serve (fixed in go1.27.3)",
		}},
		"called, with no fix": {stream: []string{config, unfixed},
			listed: []string{"GO-2026-0002: example.com/lib v1.0.0, example.com/lib.Do (no fix)"}},
		"the standard library's": {stream: []string{config, stdlib},
			failing: []string{"GO-2026-0003: stdlib go1.27.2, net/http.*Server.Serve (fixed in go1.27.3)"}},
	} {
		t.Run(name, func(t *testing.T) {
			findings, err := vulngate.Read(strings.NewReader(strings.Join(tc.stream, "\n")))
			require.NoError(t, err)
			failing, listed := vulngate.Judge(findings)
			assert.Equal(t, tc.failing, failing)
			assert.Equal(t, tc.listed, listed)
		})
	}
}

// An answer that is not govulncheck's is an error, not a clean scan.
func TestAnAnswerThatIsNotJSONIsRefused(t *testing.T) {
	_, err := vulngate.Read(strings.NewReader(config + "\nScanning your code..."))
	require.Error(t, err)
}
