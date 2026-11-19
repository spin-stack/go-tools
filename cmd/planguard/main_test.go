package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const (
	applied = `{"format_version":"1.2","resource_changes":[{"address":"aws_launch_template.cp","type":"aws_launch_template","change":{"actions":["delete","create"]}}]}`
	held    = `{"format_version":"1.2","resource_changes":[{"address":"aws_s3_bucket.logs","type":"aws_s3_bucket","change":{"actions":["delete"]}}]}`
)

// The verdict is the first line, the changes held after it, and verdict=<it> in $GITHUB_OUTPUT;
// any verdict exits 0. A type named replaceable is replaced unread.
func TestTheVerdictIsSaidAndWrittenForTheWorkflow(t *testing.T) {
	out := filepath.Join(t.TempDir(), "output")
	t.Setenv("GITHUB_OUTPUT", out)
	for _, tc := range []struct {
		args       []string
		said, kept string
	}{
		{[]string{write(t, held)}, "person\n  aws_s3_bucket.logs: delete\n", "verdict=person\n"},
		{[]string{"-replaceable", "aws_instance,aws_launch_template", write(t, applied)}, "apply\n", "verdict=person\nverdict=apply\n"},
		{[]string{write(t, applied)}, "person\n  aws_launch_template.cp: delete, create\n", "verdict=person\nverdict=apply\nverdict=person\n"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(tc.args, &stdout, &stderr); code != 0 || stdout.String() != tc.said {
			t.Errorf("%v: %d, said %q (%s), want %q", tc.args, code, stdout.String(), stderr.String(), tc.said)
		}
		if got, err := os.ReadFile(out); err != nil || string(got) != tc.kept {
			t.Errorf("%v: $GITHUB_OUTPUT has %q, %v; want %q", tc.args, got, err, tc.kept)
		}
	}
}

// No plan, two, one that cannot be read or is not a plan, an unknown flag, and a $GITHUB_OUTPUT that
// cannot be written are no answer: exit 2, nothing on stdout.
func TestWhatGivesNoAnswerExitsTwo(t *testing.T) {
	t.Setenv("GITHUB_OUTPUT", "")
	for name, args := range map[string][]string{
		"no plan":    nil,
		"two":        {write(t, held), write(t, held)},
		"none there": {filepath.Join(t.TempDir(), "none.json")},
		"not a plan": {write(t, "{}")},
		"a flag not": {"-x", write(t, held)},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 || stdout.Len() != 0 {
			t.Errorf("%s: %d, said %q", name, code, stdout.String())
		}
	}
	for what, path := range map[string]string{"a directory": t.TempDir(), "a file that takes no write": "/dev/full"} {
		t.Setenv("GITHUB_OUTPUT", path)
		var stdout, stderr bytes.Buffer
		if code := run([]string{write(t, held)}, &stdout, &stderr); code != 2 {
			t.Errorf("a $GITHUB_OUTPUT that is %s: %d", what, code)
		}
	}
}
