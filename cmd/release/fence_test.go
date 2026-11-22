package main

import (
	"os"
	"testing"

	"github.com/spin-stack/go-tools/internal/gittest"
)

// The command runs git, and a git of it that misses its repository - a mutant - finds the
// checkout's: fenced first, it finds none (internal/gittest).
func TestMain(m *testing.M) {
	gittest.Fence()
	os.Exit(m.Run())
}
