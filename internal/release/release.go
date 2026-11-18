// Package release names go-tools' releases as spin-stack names its own: v<YYYYMMDD>.<NN>, the
// day in UTC and which release of that day (spin's hack/releaseversion and docs/releases.md). A
// release is made of main each time its gate passes, so a day can have several.
//
// A name of that form is no module version - semver is three numbers - so each release is also
// tagged as one (Module): what a repository's go.mod names, and what Dependabot moves.
package release

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// name is a release's name, and nothing that only resembles one: two digits of sequence, never
// one, so v20261109.1 and v20261109.01 are not two spellings of one release.
var name = regexp.MustCompile(`^v(\d{8})\.(\d{2})$`)

// Next is the name of the next release on now's day, given every tag there is.
func Next(tags []string, now time.Time) string {
	day := now.UTC().Format("20060102")
	next := 1
	for _, t := range tags {
		m := name.FindStringSubmatch(t)
		if m == nil || m[1] != day {
			continue
		}
		if seq, _ := strconv.Atoi(m[2]); seq >= next {
			next = seq + 1
		}
	}
	return fmt.Sprintf("v%s.%02d", day, next)
}

// Module is the module version release is also tagged as: v0.<YYYYMMDD>.<N>, which Go and
// Dependabot order as the releases are ordered. The sequence loses its leading zero, which semver
// refuses. The day is the minor, not the major: a major past 1 is a module path that ends in it.
func Module(release string) (string, error) {
	m := name.FindStringSubmatch(release)
	if m == nil {
		return "", fmt.Errorf("release: %q is not v<YYYYMMDD>.<NN>", release)
	}
	seq, _ := strconv.Atoi(m[2]) //nolint:errcheck // two digits, by the pattern
	return fmt.Sprintf("v0.%s.%d", m[1], seq), nil
}

// Of is the release among tags, the ones that point at a commit, that names it; "" when the
// commit is not released. A run asked again of a commit it released answers that release rather
// than making a second one of the same bytes.
func Of(tags []string) string {
	best := ""
	for _, t := range tags {
		if name.MatchString(t) && t > best {
			best = t
		}
	}
	return best
}
