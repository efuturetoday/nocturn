package capability

import (
	"sync"
	"time"
)

// RateLimiter enforces "at most N calls per window" PER KEY (a capability family),
// with a DIFFERENT limit per key and — crucially — no limit at all for keys that were
// never configured. It is a sliding window: a call is allowed only if fewer than the
// key's limit fall within its trailing window.
//
// Per-key-with-unlimited-default is what lets the broker rate only the families that
// need it (e.g. "notify" — reaching the user's device) while bursty reads (http.read,
// file.read) pass freely. The gateway consults it on every authorized path
// (gateway.Guard.Rate); it is deliberately NOT part of the pure capability.Env.
type RateLimiter struct {
	limits map[string]rateCfg // key -> config; a key absent here is UNLIMITED
	mu     sync.Mutex
	events map[string][]time.Time
}

// rateCfg is one key's cap: at most limit calls per window.
type rateCfg struct {
	limit  int
	window time.Duration
}

// RateLimiterOption configures a RateLimiter.
type RateLimiterOption func(*RateLimiter)

// WithLimit caps key at limit calls per window. A key with no WithLimit is UNLIMITED
// (Allow always true), so only the families you name here are rate-limited.
func WithLimit(key string, limit int, window time.Duration) RateLimiterOption {
	return func(r *RateLimiter) { r.limits[key] = rateCfg{limit: limit, window: window} }
}

// NewRateLimiter builds a limiter; configure each rate-limited key with WithLimit.
// With no WithLimit options, every key is unlimited (Allow always true). It reads the
// real clock (time.Now) — time-dependent tests drive it under testing/synctest, not a
// clock seam (see go.dev/blog/testing-time).
func NewRateLimiter(opts ...RateLimiterOption) *RateLimiter {
	r := &RateLimiter{
		limits: make(map[string]rateCfg),
		events: make(map[string][]time.Time),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Allow reports whether a call for key is within its limit, recording it if so. When it
// is NOT allowed, retryAfter is how long until the oldest in-window call ages out and a
// slot frees — so a caller can tell the model when to try again (or schedule a wake). A
// key with no configured limit is always allowed (retryAfter 0, not tracked). A denied
// call is NOT recorded, so being over the limit never pushes the window forward.
func (r *RateLimiter) Allow(key string) (allowed bool, retryAfter time.Duration) {
	cfg, limited := r.limits[key] // set once at construction, never mutated → lock-free read
	if !limited {
		return true, 0 // unconfigured key: unlimited
	}

	now := time.Now()
	cutoff := now.Add(-cfg.window)

	r.mu.Lock()
	defer r.mu.Unlock()

	recent := make([]time.Time, 0, len(r.events[key]))
	for _, t := range r.events[key] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	if len(recent) >= cfg.limit {
		r.events[key] = recent
		if len(recent) == 0 {
			return false, cfg.window // a limit of 0 blocks everything; no call to age out
		}
		// The oldest in-window call ages out at recent[0]+window → a slot frees then.
		return false, recent[0].Add(cfg.window).Sub(now)
	}
	r.events[key] = append(recent, now)
	return true, 0
}
