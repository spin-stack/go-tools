package main

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func fakeGit(atCommit, all []string, failOn string) gitFunc {
	return func(args ...string) ([]string, error) {
		if slices.Contains(args, failOn) {
			return nil, errors.New("git failed")
		}
		if slices.Contains(args, "--points-at") {
			return atCommit, nil
		}
		return all, nil
	}
}

// A commit released already is that release; one that is not is the day's next.
func TestACommitIsItsReleaseOrTheNextOne(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		at, all []string
		want    string
	}{
		{"released", []string{"v20261110.02"}, []string{"v20261110.01", "v20261110.02", "v20261110.03"}, "version=v20261110.02\nnew=false\n"},
		{"not yet", nil, []string{"v20261110.01", "v20261110.02"}, "version=v20261110.03\nnew=true\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := run([]string{"abc"}, &out, fakeGit(tc.at, tc.all, ""), now); err != nil {
				t.Fatal(err)
			}
			if out.String() != tc.want {
				t.Errorf("said %q, want %q", out.String(), tc.want)
			}
		})
	}
}

// No commit, or a git that fails, is no name.
func TestNoCommitOrNoGitIsNoName(t *testing.T) {
	var out bytes.Buffer
	if err := run(nil, &out, fakeGit(nil, nil, ""), time.Now()); err == nil || out.Len() != 0 {
		t.Errorf("no commit: %v, %q", err, out.String())
	}
	for _, failOn := range []string{"--points-at", "--list"} {
		out.Reset()
		if err := run([]string{"abc"}, &out, fakeGit(nil, nil, failOn), time.Now()); err == nil || out.Len() != 0 {
			t.Errorf("git failing on %s: %v, %q", failOn, err, out.String())
		}
	}
}

// git's own output is read a word a line, and its failure says what it said.
func TestGitIsTheClonesGit(t *testing.T) {
	tags, err := git("tag", "--list", "no-such-tag-*")
	if err != nil || len(tags) != 0 {
		t.Errorf("tags = %v, %v", tags, err)
	}
	if _, err := git("no-such-command"); err == nil || !strings.Contains(err.Error(), "git no-such-command") {
		t.Errorf("err = %v", err)
	}
}
