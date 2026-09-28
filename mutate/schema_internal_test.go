package mutate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// An error the compiler reports in a schema is put on the edit it is about: the site that starts
// on its line, else the smallest that spans it - an error inside a call a field's value is, say,
// is the call's and not the assignment's. One on no site, or in another file, is no edit's.
func TestACompileErrorIsPutOnTheSiteItIsIn(t *testing.T) {
	outer := &site{start: 10, end: 90, from: 3, to: 6}
	mid := &site{start: 20, end: 80, from: 3, to: 6}
	inner := &site{start: 30, end: 40, from: 4, to: 4}
	late := &site{start: 50, end: 60, from: 5, to: 5}
	// In both orders: which site is found first is the plan's order, and says nothing.
	var built, backwards []Mutation
	for _, s := range []*site{outer, mid, inner, late} {
		built = append(built, Mutation{File: "/repo/p/a.go", site: s})
		backwards = append([]Mutation{{File: "/repo/p/a.go", site: s}}, backwards...)
	}
	for name, c := range map[string]struct {
		out  string
		want []*site
	}{
		"on the line it starts, inside others":  {"./p/a.go:4:2: cannot use x\n", []*site{inner}},
		"on the line one starts":                {"a.go:5:1: undefined: y\n", []*site{late}},
		"on a line two only span, the smaller":  {"# p\n/repo/p/a.go:6:9: z\n", []*site{mid}},
		"on the line two start, the smaller":    {"a.go:3:9: z\n", []*site{mid}},
		"two errors, two sites":                 {"a.go:4:2: x\na.go:5:1: y\n", []*site{inner, late}},
		"in another file":                       {"b.go:4:2: x\n", nil},
		"on no site":                            {"a.go:9:1: x\n", nil},
		"one on a site and one on none is none": {"a.go:4:2: x\na.go:9:1: y\n", nil},
	} {
		t.Run(name, func(t *testing.T) {
			for _, order := range [][]Mutation{built, backwards} {
				var sites []*site
				for s := range refusedBy(c.out, order) {
					sites = append(sites, s)
				}
				assert.ElementsMatch(t, c.want, sites)
			}
		})
	}
}

// What a failing test printed is shown by its last lines, all of them when there are fewer.
func TestTheTailOfWhatATestSaidIsItsLastLines(t *testing.T) {
	for out, want := range map[string]string{
		"a\nb\nc\n": "b\nc",
		"a\nb":      "a\nb",
		"a\n":       "a",
	} {
		assert.Equal(t, want, tail([]byte(out), 2), "the tail of %q", out)
	}
}
