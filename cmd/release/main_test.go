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

// The next release is named by its day in UTC and its turn, counting only releases' names, and a
// commit a release names is refused: what `task release` would tag.
func TestTheNextNameIsTheDaysTurnAndAReleasedCommitIsRefused(t *testing.T) {
	// 22 November in UTC, and still the 21st in Buenos Aires.
	now := time.Date(2026, 11, 22, 1, 30, 0, 0, time.FixedZone("ART", -3*3600))
	for _, tc := range []struct {
		name    string
		at, all []string
		want    string
		refuse  string
	}{
		{name: "the first there is", want: "v20261122.01\n"},
		{name: "the third of a day", all: []string{"v20261122.02", "v20261122.01", "v20261121.07"}, want: "v20261122.03\n"},
		{
			name: "brand's screens, a one-digit tag and a module version are no releases",
			at:   []string{"screens-v20261122.01-c2f0602021cc", "v0.20261122.5"},
			all:  []string{"screens-v20261122.01-c2f0602021cc", "v20261122.4", "v0.20261122.5"},
			want: "v20261122.01\n",
		},
		{name: "a commit released already", at: []string{"v20261121.02"}, all: []string{"v20261121.02"}, refuse: "abc is released already, as v20261121.02"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := run([]string{"abc"}, &out, fakeGit(tc.at, tc.all, ""), now)
			if tc.refuse != "" {
				if err == nil || err.Error() != tc.refuse || out.Len() != 0 {
					t.Fatalf("got %v and %q, want the refusal %q", err, out.String(), tc.refuse)
				}
				return
			}
			if err != nil || out.String() != tc.want {
				t.Fatalf("said %q, %v; want %q", out.String(), err, tc.want)
			}
		})
	}
}

// For a workflow, a commit released already is that release; one that is not is the day's next.
func TestACommitIsItsReleaseOrTheNextOne(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		at, all []string
		want    string
	}{
		{"released", []string{"v20261110.02"}, []string{"v20261110.01", "v20261110.02", "v20261110.03"}, "version=v20261110.02\nnew=false\nmodule=v0.20261110.2\n"},
		{"not yet", nil, []string{"v20261110.01", "v20261110.02"}, "version=v20261110.03\nnew=true\nmodule=v0.20261110.3\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := run([]string{"-outputs", "abc"}, &out, fakeGit(tc.at, tc.all, ""), now); err != nil {
				t.Fatal(err)
			}
			if out.String() != tc.want {
				t.Errorf("said %q, want %q", out.String(), tc.want)
			}
		})
	}
}

// No commit, two, a flag there is not, or a git that fails, is no name.
func TestNoCommitOrNoGitIsNoName(t *testing.T) {
	var out bytes.Buffer
	for _, args := range [][]string{nil, {"-outputs"}, {"abc", "def"}, {"-no-such-flag", "abc"}} {
		if err := run(args, &out, fakeGit(nil, nil, ""), time.Now()); err == nil || out.Len() != 0 {
			t.Errorf("%v: %v, %q", args, err, out.String())
		}
	}
	for _, failOn := range []string{"--points-at", "--list"} {
		for _, args := range [][]string{{"abc"}, {"-outputs", "abc"}} {
			out.Reset()
			if err := run(args, &out, fakeGit(nil, nil, failOn), time.Now()); err == nil || out.Len() != 0 {
				t.Errorf("%v with git failing on %s: %v, %q", args, failOn, err, out.String())
			}
		}
	}
}

// git's own output is read a word a line, and its failure says what it said.
func TestGitIsTheClonesGit(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "x"},
		{"tag", "v20261110.01"}, {"tag", "v20261110.02"}} {
		if _, err := git(t.Context(), args...); err != nil {
			t.Fatal(err)
		}
	}
	tags, err := git(t.Context(), "tag", "--list")
	if err != nil || !slices.Equal(tags, []string{"v20261110.01", "v20261110.02"}) {
		t.Errorf("tags = %v, %v", tags, err)
	}
	if _, err := git(t.Context(), "no-such-command"); err == nil || !strings.Contains(err.Error(), "git no-such-command") {
		t.Errorf("err = %v", err)
	}
}
