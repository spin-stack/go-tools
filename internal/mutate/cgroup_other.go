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

func confined(string, *exec.Cmd) (bool, error) { return false, nil }
