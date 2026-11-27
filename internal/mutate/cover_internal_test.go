package mutate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A profile names every package the binary measured, and only this package's own files are about
// the edit: a subpackage's file of the same name, or another package's, is not.
func TestAProfileIsReadForThePackagesOwnFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.cov")
	require.NoError(t, os.WriteFile(path, []byte(`mode: set
example.com/m/p/code.go:3.20,5.2 1 1
example.com/m/p/code.go:7.18,9.2 1 0
example.com/m/p/sub/code.go:11.1,12.2 1 1
example.com/m/other/code.go:13.1,14.2 1 1
example.com/m/p/code.go:15.1 1 1
example.com/m/p/code.go:x.1,16.2 1 1
example.com/m/p/code.go:17.1,y.2 1 1
example.com/m/p/code.go:18.z,19.2 1 1
`), 0o600))
	executed, all, err := readProfile(path, "example.com/m/p/")
	require.NoError(t, err)
	assert.Equal(t, map[string][]span{"code.go": {{place{3, 20}, place{5, 2}}}}, executed)
	assert.Equal(t, map[string][]span{"code.go": {{place{3, 20}, place{5, 2}}, {place{7, 18}, place{9, 2}}}}, all)

	_, _, err = readProfile(filepath.Join(t.TempDir(), "none.cov"), "example.com/m/p/")
	assert.Error(t, err)
}

// An edit is asked of the tests that execute a block holding it, to the column: of a line two
// blocks share, the one it is in. One no block holds - a switch's case - is asked of the tests that
// execute the block of the first statement around it that one holds, and one around which no
// block holds anything, of every test. A place in a block no test executed is reached by none.
func TestAnEditIsAskedOfTheTestsOfTheBlockThatHoldsIt(t *testing.T) {
	// 3	switch x {
	// 4	case 1: one()
	// 5	case 2:
	// 6		two()
	// 7	}
	// 8	never()
	header, one, two := span{place{3, 2}, place{3, 11}}, span{place{4, 10}, place{4, 15}}, span{place{6, 3}, place{6, 8}}
	c := &Coverage{
		blocks: map[string][]span{"code.go": {header, one, two, {place{8, 2}, place{8, 9}}}},
		reached: map[string]map[string][]span{"code.go": {
			"TestOne": {header, one}, "TestTwo": {header, two}, "TestNeither": {header},
		}},
	}
	edit := func(file string, reach ...place) Mutation { return Mutation{File: file, reach: reach} }
	every := []string{"TestNeither", "TestOne", "TestTwo"}
	assert.Equal(t, []string{"TestOne"}, c.Reaching(edit("dir/code.go", place{4, 10}, place{3, 2})), "the case's body")
	assert.Equal(t, every, c.Reaching(edit("code.go", place{4, 7}, place{4, 2}, place{3, 2})),
		"the case on line 4 was asked of the tests of the block beside it")
	assert.Equal(t, every, c.Reaching(edit("code.go", place{5, 7}, place{5, 2}, place{3, 2})))
	assert.Equal(t, []string{}, c.Reaching(edit("code.go", place{8, 2})), "a block nothing executes")
	assert.Nil(t, c.Reaching(edit("code.go", place{5, 7}, place{5, 2})), "nothing around the edit is in a block")
	assert.Nil(t, c.Reaching(edit("code.go", place{3, 11})), "a block's end is in it")
	assert.Nil(t, c.Reaching(edit("other.go", place{3, 2})))
	assert.Nil(t, c.Reaching(Mutation{File: "code.go"}), "an edit not planned")
}
