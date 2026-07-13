package capability

import (
	"sync"
	"time"
)

// RateLimiter enforces "at most N calls per window" for a key (e.g. a
// capability name, or capability+host). It is a sliding window: a call is
// allowed only if fewer than limit calls fall within the trailing window.
//
// Like EpochRegistry it is a standalone stateful mechanism the broker consults;
// wiring it into evaluation comes when the broker's contextual inputs (epoch
// liveness, clock, rate state) are consolidated.
type RateLimiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu     sync.Mutex
	events map[string][]time.Time
}

// RateLimiterOption configures a RateLimiter.
type RateLimiterOption func(*RateLimiter)

// WithClock overrides the time source. Used in tests for determinism.
func WithClock(now func() time.Time) RateLimiterOption {
	return func(r *RateLimiter) { r.now = now }
}

// NewRateLimiter allows at most limit calls per key within window.
func NewRateLimiter(limit int, window time.Duration, opts ...RateLimiterOption) *RateLimiter {
	r := &RateLimiter{
		limit:  limit,
		window: window,
		now:    time.Now,
		events: make(map[string][]time.Time),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Allow reports whether a call for key is within the limit, recording it if so.
// Calls older than the window are forgotten. A denied call is NOT recorded, so
// being over the limit never pushes the window forward.
func (r *RateLimiter) Allow(key string) bool {
	now := r.now()
	cutoff := now.Add(-r.window)

	r.mu.Lock()
	defer r.mu.Unlock()

	recent := make([]time.Time, 0, len(r.events[key]))
	for _, t := range r.events[key] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	if len(recent) >= r.limit {
		r.events[key] = recent
		return false
	}
	r.events[key] = append(recent, now)
	return true
}
