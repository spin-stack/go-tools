//go:build !linux

package ghatrace

import "os"

// rusage adds nothing off Linux: another kernel counts a resident set and blocks in units of its
// own, and ghatrace's runners are Linux.
func rusage(*os.ProcessState, *Usage) {}
