package linkweight

import (
	"bytes"
	"strings"
	"testing"
)

func TestJudgeHoldsBothDirections(t *testing.T) {
	b := Analyze(fixture(t))
	allowed := map[string]bool{
		"cmd/a capability exec":              true,
		"cmd/a capability listen":            true,
		"cmd/a reflect-method text/template": true,
		"cmd/a capability mount":             true,
	}
	v := Judge([]Binary{b}, allowed)
	if v.OK() {
		t.Fatal("a capability no line explains and a line nothing matches passed")
	}
	if len(v.Unexplained) != 1 || v.Unexplained[0] != "cmd/a capability reflect-call" {
		t.Errorf("unexplained = %v", v.Unexplained)
	}
	if len(v.Stale) != 1 || v.Stale[0] != "cmd/a capability mount" {
		t.Errorf("stale = %v", v.Stale)
	}
	var out bytes.Buffer
	v.Write(&out, "allow.txt")
	for _, want := range []string{"no line of allow.txt says why:\n  cmd/a capability reflect-call", "no binary reaches any more", "  cmd/a capability mount"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the verdict does not say %q:\n%s", want, out.String())
		}
	}

	delete(allowed, "cmd/a capability mount")
	allowed["cmd/a capability reflect-call"] = true
	v = Judge([]Binary{b}, allowed)
	out.Reset()
	v.Write(&out, "allow.txt")
	if !v.OK() || !strings.Contains(out.String(), "OK:") {
		t.Errorf("every finding explained and every line used: %+v\n%s", v, out.String())
	}
}

func TestWriteSaysWhatABinaryLinksAndWhy(t *testing.T) {
	b := Analyze(fixture(t))
	b.Breakers = append(b.Breakers, Finding{Kind: KindReflectMethod, Name: "encoding/json/v2", Chain: []string{"cmd/a", "encoding/json/v2"}, ViaMethod: true})
	on := Binary{Root: "cmd/b", MethodElimination: true, Modules: []Weight{{"std", 1 << 20}}}
	var out bytes.Buffer
	Write(&out, []Binary{b, on}, 1)
	got := out.String()
	for _, want := range []string{
		"cmd/a: 4.0 MB shipped, 3 modules, 5 packages",
		"method elimination: OFF",
		"    text/template                cmd/a > lib > text/template\n",
		"possibly only because elimination is already off:\n    encoding/json/v2",
		"    exec                         cmd/a > cloud > example.com/sdk/svc > os\n",
		"      2.5 MB  example.com/sdk\n  imports",
		"      2.5 MB  cloud > example.com/sdk/svc  (-1 modules)\n\ncmd/b",
		"cmd/b: 0.0 MB shipped",
		"  method elimination: on\n  capabilities: none\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report does not say %q:\n%s", want, got)
		}
	}
	// top bounds the lists: one module and one cut.
	if strings.Contains(got, "MB  std\n  imports") || strings.Count(got, " > example.com/sdk/svc  (-1") != 1 {
		t.Errorf("top 1 listed more:\n%s", got)
	}
}
