package release

import (
	"testing"
	"time"
)

// The next release is the day's next number, whatever else is tagged: another day's, a name that
// only resembles one, a prerelease.
func TestTheNextReleaseIsTheDaysNextNumber(t *testing.T) {
	now := time.Date(2026, 11, 9, 23, 30, 0, 0, time.FixedZone("ART", -3*3600)) // 2026-11-10 in UTC
	for _, tc := range []struct {
		name string
		tags []string
		want string
	}{
		{"no release yet", nil, "v20261110.01"},
		{"another day's", []string{"v20261109.07"}, "v20261110.01"},
		{"the day's", []string{"v20261110.01", "v20261110.02", "v20261109.09"}, "v20261110.03"},
		{"out of order", []string{"v20261110.09", "v20261110.02"}, "v20261110.10"},
		{"not a release's name", []string{"v20261110.3", "v20261110.04-rc1", "bin-20261110.212154", "v1.2.3"}, "v20261110.01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Next(tc.tags, now); got != tc.want {
				t.Errorf("Next = %s, want %s", got, tc.want)
			}
		})
	}
}

// A commit's release is the release tag pointing at it, the latest when it has two; none when no
// tag at it is a release's name.
func TestACommitsReleaseIsTheReleaseTagAtIt(t *testing.T) {
	for _, tc := range []struct {
		tags []string
		want string
	}{
		{nil, ""},
		{[]string{"bin-20261109.212154", "v1.0.0"}, ""},
		{[]string{"v20261109.01"}, "v20261109.01"},
		{[]string{"v20261109.01", "v20261110.02", "v20261110.1"}, "v20261110.02"},
	} {
		if got := Of(tc.tags); got != tc.want {
			t.Errorf("Of(%v) = %q, want %q", tc.tags, got, tc.want)
		}
	}
}
