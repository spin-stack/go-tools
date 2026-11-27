//go:build !windows

package main

import (
	"context"
	"syscall"
)

// execTask replaces this process with Task's: a signal, the terminal and the exit status are
// Task's, with nothing of this one left between them.
func execTask(_ context.Context, path string, argv, env []string) error {
	return syscall.Exec(path, argv, env) //nolint:gosec // the Task this command installed and checked against its pin
}
