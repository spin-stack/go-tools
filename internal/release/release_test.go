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
		{"not a release's name", []string{"v20261110.3", "v20261110.04-rc1", "bin-20261110.212154", "v1.2.3", "v0.20261110.4"}, "v20261110.01"},
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
		{[]string{"v0.20261110.2"}, ""},
	} {
		if got := Of(tc.tags); got != tc.want {
			t.Errorf("Of(%v) = %q, want %q", tc.tags, got, tc.want)
		}
	}
}

// A release's module version is its day and its number, which semver reads in the releases'
// order; what is not a release's name has none.
func TestAReleaseIsAlsoAModuleVersion(t *testing.T) {
	for release, want := range map[string]string{
		"v20261110.01": "v0.20261110.1",
		"v20261110.10": "v0.20261110.10",
	} {
		if got, err := Module(release); err != nil || got != want {
			t.Errorf("Module(%s) = %q, %v; want %q", release, got, err, want)
		}
	}
	for _, notOne := range []string{"v20261110.1", "v0.20261110.1", "20261110.01", ""} {
		if got, err := Module(notOne); err == nil {
			t.Errorf("Module(%q) = %q, want an error", notOne, got)
		}
	}
}

// A module version names the release it is the tag of, and a version that is no release's -
// a commit after one, a checkout's build, a release's own name - names none.
func TestAModuleVersionNamesItsRelease(t *testing.T) {
	for version, want := range map[string]string{
		"v0.20261110.1":  "v20261110.01",
		"v0.20261110.10": "v20261110.10",
		"v0.20261122.5-0.20261122181500-ede9302abcde": "",
		"v0.20261122.4+dirty":                         "",
		"v0.20261110.100":                             "",
		"v1.20261110.1":                               "",
		"xv0.20261110.1":                              "",
		"v20261110.01":                                "",
		"(devel)":                                     "",
		"":                                            "",
	} {
		if got := Named(version); got != want {
			t.Errorf("Named(%q) = %q, want %q", version, got, want)
		}
	}
}
