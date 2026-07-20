package agent

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// Scheduler fires agents on their cron schedule. It ticks once a minute (aligned to the minute
// boundary) and, for each agent whose When matches, calls fire. agentkit has no notion of time, so
// scheduling lives here; execution is the caller's, injected as fire.
type Scheduler struct {
	agents Set
	fire   func(ctx context.Context, a Agent)
}

// NewScheduler builds a Scheduler over a Set, calling fire for each agent whose schedule matches.
func NewScheduler(agents Set, fire func(ctx context.Context, a Agent)) *Scheduler {
	return &Scheduler{agents: agents, fire: fire}
}

// Start runs the tick loop until ctx is cancelled. It aligns to the next minute so a "* * * * *"
// agent fires at :00, then every minute.
func (s *Scheduler) Start(ctx context.Context) {
	for {
		next := time.Now().Truncate(time.Minute).Add(time.Minute)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.tick(ctx, next)
		}
	}
}

// tick fires every agent whose schedule matches t.
func (s *Scheduler) tick(ctx context.Context, t time.Time) {
	for _, a := range s.agents {
		if a.When != "" && cronMatches(a.When, t) {
			s.fire(ctx, a)
		}
	}
}

// cronMatches reports whether a 5-field cron spec (minute hour day-of-month month day-of-week)
// matches t. Each field supports "*", a number, "a-b" ranges, "*/n" or "a-b/n" steps, and
// comma-separated lists of those. Day-of-week is 0-6 with Sunday=0.
func cronMatches(spec string, t time.Time) bool {
	f := strings.Fields(spec)
	if len(f) != 5 {
		return false
	}
	return fieldMatch(f[0], t.Minute(), 0, 59) &&
		fieldMatch(f[1], t.Hour(), 0, 23) &&
		fieldMatch(f[2], t.Day(), 1, 31) &&
		fieldMatch(f[3], int(t.Month()), 1, 12) &&
		fieldMatch(f[4], int(t.Weekday()), 0, 6)
}

// fieldMatch matches one cron field (a comma list of parts) against val within [min,max].
func fieldMatch(field string, val, min, max int) bool {
	for part := range strings.SplitSeq(field, ",") {
		if partMatch(part, val, min, max) {
			return true
		}
	}
	return false
}

// partMatch matches one cron part: "*", "n", "a-b", or any of those with a "/step" suffix.
func partMatch(part string, val, min, max int) bool {
	step := 1
	if base, stepStr, ok := strings.Cut(part, "/"); ok {
		s, err := strconv.Atoi(stepStr)
		if err != nil || s <= 0 {
			return false
		}
		step = s
		part = base
	}

	lo, hi := min, max
	switch {
	case part == "*":
		// full range
	case strings.ContainsRune(part, '-'):
		a, b, ok := strings.Cut(part, "-")
		loN, err1 := strconv.Atoi(a)
		hiN, err2 := strconv.Atoi(b)
		if !ok || err1 != nil || err2 != nil {
			return false
		}
		lo, hi = loN, hiN
	default:
		n, err := strconv.Atoi(part)
		if err != nil {
			return false
		}
		lo, hi = n, n
	}

	if val < lo || val > hi {
		return false
	}
	return (val-lo)%step == 0
}
