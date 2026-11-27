package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// background runs this program with args in a session of its own, with nothing to read or write
// to the step, so the step ends without waiting for it. It keeps the job's environment, and with
// it RUNNER_TRACKING_ID, by which the runner ends it with the job.
func background(path string, args ...string) error {
	unlock, err := lock(path + ".sampling")
	if err != nil {
		return err
	}
	unlock() // the sampler takes it
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, args...) //nolint:gosec // this program, again
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

var errSampled = errors.New("this job's machine is sampled already")

// lock takes the lock at path, or is errSampled when another process holds it. The lock is the
// kernel's, so a sampler that is killed lets it go.
func lock(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644) //nolint:gosec // beside the job's span file
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { //nolint:gosec // a descriptor fits an int
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errSampled
		}
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}
