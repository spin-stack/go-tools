//go:build !linux

package mutate

import (
	"context"
	"os/exec"
)

// testCgroup is never set where there are no cgroups: a test's memory is measured from /proc.
var testCgroup string

// pidsMax bounds nothing where there are no cgroups.
var pidsMax = 0

func delegated(context.Context) (string, error) { return "", nil }

// confined panics: it is not reached while testCgroup is never set here, and a return would read
// as a test that passed, or as one that failed, without its having run.
func confined(string, *exec.Cmd) (bool, error) {
	panic("mutate: a test process confined to a cgroup where there are none")
}
