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
