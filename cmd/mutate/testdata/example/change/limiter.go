// Package ratelimit lets a call through once per interval.
package ratelimit

import "time"

// Limiter lets one call through per interval.
type Limiter struct {
	every time.Duration
	last  time.Time
}

// New is a limiter that lets one call through every interval.
func New(every time.Duration) *Limiter {
	return &Limiter{every: every}
}

// Allow says whether a call at now may go ahead, and counts it when it does.
func (l *Limiter) Allow(now time.Time) bool {
	if now.Sub(l.last) < l.every {
		return false
	}
	l.last = now
	return true
}
