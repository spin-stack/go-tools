//go:build !linux

package main

import (
	"context"
	"errors"
)

// background is Linux's alone: no other system has the /proc a sample reads.
func background(context.Context, string, ...string) error {
	return errors.New("ghatrace samples Linux alone")
}

// lock holds nothing off Linux, where nothing is sampled.
func lock(string) (func(), error) { return func() {}, nil }

var errSampled = errors.New("this job's machine is sampled already")
