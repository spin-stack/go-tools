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
