package ratelimit

import (
	"testing"
	"time"
)

func TestTheFirstCallIsAllowed(t *testing.T) {
	if !New(time.Second).Allow(time.Now()) {
		t.Fatal("the first call was refused")
	}
}

func TestACallWithinTheIntervalIsRefused(t *testing.T) {
	now := time.Now()
	l := &Limiter{every: time.Second, last: now}
	if l.Allow(now.Add(time.Millisecond)) {
		t.Fatal("a call a millisecond after the last was allowed")
	}
}

// The limiter remembers the call it let through: the next one, a millisecond later, waits.
func TestAnAllowedCallStartsTheInterval(t *testing.T) {
	now := time.Now()
	l := New(time.Second)
	if !l.Allow(now) {
		t.Fatal("the first call was refused")
	}
	if l.Allow(now.Add(time.Millisecond)) {
		t.Fatal("a second call a millisecond after the first was allowed")
	}
}
