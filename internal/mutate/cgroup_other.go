//go:build !linux

package mutate

import "os/exec"

// testCgroup is never set where there are no cgroups: a test's memory is measured from /proc.
var testCgroup string

// pidsMax bounds nothing where there are no cgroups.
var pidsMax = 0

func delegated() (string, error) { return "", nil }

func confined(what string, cmd *exec.Cmd) ([]byte, bool, error) {
	out, err := combined(what, cmd)
	return out, false, err
}
