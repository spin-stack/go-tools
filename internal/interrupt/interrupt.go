// Package interrupt is the context a command runs under: cancelled by Ctrl+C or SIGTERM, so what
// it started with exec.CommandContext is stopped and its deferred cleanup runs.
package interrupt

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// Context is cancelled by the first interrupt or SIGTERM. That signal's default action is restored
// before the context is cancelled, so a second Ctrl+C ends a process whose cleanup hangs instead
// of being swallowed too; signal.NotifyContext keeps catching until its stop is called. It is a
// process's one context, from main to its exit: nothing stops listening sooner. Windows sends no
// SIGTERM; its Ctrl+C is os.Interrupt.
func Context() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		signal.Stop(sigs)
		cancel()
	}()
	return ctx
}
