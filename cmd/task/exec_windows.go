package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
)

// execTask runs Task as a process of its own, since Windows replaces no process with another, and
// exits as Task did. The console's Ctrl+C reaches Task as it reaches this one, which waits for
// Task to end rather than ending first and leaving it running.
func execTask(ctx context.Context, path string, argv, env []string) error {
	signal.Ignore(os.Interrupt)
	cmd := exec.CommandContext(ctx, path, argv[1:]...) //nolint:gosec // the Task this command installed and checked
	cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = env, os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		os.Exit(exit.ExitCode())
	}
	if err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
