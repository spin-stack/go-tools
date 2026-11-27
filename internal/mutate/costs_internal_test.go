package mutate

import (
	"os/exec"
	"strings"
	"testing"
)

// A go command killed by a cancelled run leaves its work directory behind; workIn has it make
// that directory under one the run removes, not in TMPDIR.
func TestAGoCommandWorksUnderTheRunsDirectory(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.CommandContext(t.Context(), "go", "env", "GOTMPDIR")
	workIn(cmd, dir)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != dir {
		t.Errorf("the go command works under %q, not %q", got, dir)
	}
}
