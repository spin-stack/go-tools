// Package waits holds tests that sleep for a second or more where a condition should be waited
// for, and the sleeps that are not that.
package waits

import (
	"testing"
	"time"
)

const settle = 3 * time.Second

func TestSleepsASecond(t *testing.T) {
	time.Sleep(time.Second) // want `TestSleepsASecond sleeps 1s`
	if t.Name() == "" {
		t.Fatal("named")
	}
}

func TestSleepsInASubtestAndAGoroutine(t *testing.T) {
	t.Run("sub", func(t *testing.T) {
		time.Sleep(2 * time.Second) // want `TestSleepsInASubtestAndAGoroutine sleeps 2s`
		if t.Name() == "" {
			t.Fatal("named")
		}
	})
	go func() {
		time.Sleep(settle) // want `TestSleepsInASubtestAndAGoroutine sleeps 3s`
	}()
	time.Sleep(time.Minute + 500*time.Millisecond) // want `TestSleepsInASubtestAndAGoroutine sleeps 1m0.5s`
	if t.Name() == "" {
		t.Fatal("named")
	}
}

// Under a second paces a loop, and a duration the test computes is not a number to judge here.
func TestPacesAndComputes(t *testing.T) {
	time.Sleep(20 * time.Millisecond)
	time.Sleep(999 * time.Millisecond)
	d := time.Duration(t.Name()[0]) * time.Second
	time.Sleep(d)
	time.Sleep(backoff(3))
	waitABit(t)
	// Not time.Sleep: another package's Sleep, and a duration handed to something else.
	clock{}.Sleep(5 * time.Second)
	<-time.After(2 * time.Second)
	if t.Name() == "" {
		t.Fatal("named")
	}
}

// A helper's sleep is the helper's: a helper named by many tests is one line to fix.
func waitABit(t *testing.T) {
	t.Helper()
	time.Sleep(5 * time.Second)
}

func backoff(n int) time.Duration { return time.Duration(n) * time.Second }

type clock struct{}

func (clock) Sleep(time.Duration) {}
