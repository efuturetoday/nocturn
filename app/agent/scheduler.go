package agent

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// catchUpGrace is how late a firing may be and still run. Timers run on the monotonic clock, which
// stops while the machine sleeps, so a due time can be well in the past by the time the loop wakes.
// A firing inside this window still runs (late but useful); one outside is logged and skipped —
// a 03:00 report fired at 09:00 is worse than one not fired at all.
const catchUpGrace = 15 * time.Minute

// maxScanYears bounds nextAfter's forward search so an unsatisfiable spec ("0 0 30 2 *" — February
// 30th) terminates instead of looping forever.
const maxScanYears = 5

// Scheduler fires agents on their cron schedule. It holds each agent's next due time, computed from
// the WALL clock, and sleeps until the earliest — it does not poll. agentkit has no notion of time,
// so scheduling lives here; execution is the caller's, injected as fire.
type Scheduler struct {
	agents Set
	fire   func(ctx context.Context, a Agent)
	log    *slog.Logger
}

// NewScheduler builds a Scheduler over a Set, calling fire for each agent whose schedule matches.
func NewScheduler(agents Set, log *slog.Logger, fire func(ctx context.Context, a Agent)) *Scheduler {
	return &Scheduler{agents: agents, fire: fire, log: log.With("component", "scheduler")}
}

// Start schedules until ctx is cancelled. It computes each agent's next due time and sleeps until
// the earliest one, rather than waking every minute to test every schedule: a sampling loop misses
// everything between samples, and the samples stop entirely while the machine sleeps.
//
// Every due time is derived from the wall clock, so a sleep or clock jump is absorbed — on wake the
// overdue agents are simply due, and catchUpGrace decides whether each still runs. An agent that
// missed several firings in one gap fires once, not once per missed slot.
//
// Startup does not catch up: due times are computed forward from now, so a schedule missed while the
// daemon was down does not fire at boot (that would need a persisted last-run record).
//
// Schedules are local wall-clock times, so daylight-saving transitions are visible. A fall-back
// repeats an hour: firing the same wall-clock minute twice is suppressed (see fireDue), so a daily
// agent still runs once. A spring-forward skips an hour outright: an agent scheduled inside the
// skipped hour has no wall-clock minute to run at that day and does not run. Removing the second
// case would mean scheduling in UTC, which changes what every existing cron spec means.
func (s *Scheduler) Start(ctx context.Context) {
	// Sorted once: fireDue reuses it every wake, and a stable order keeps log output comparable
	// between runs (ranging the Set directly would scramble it).
	agents := s.agents.All()

	// due[name] is when that agent fires next. Building it also validates: a typo'd cron silently
	// never matches, so surface it here rather than let the agent quietly never fire.
	due := map[string]time.Time{}
	now := time.Now()
	for _, a := range agents {
		if a.When == "" {
			continue // manual only
		}
		if !validCron(a.When) {
			s.log.Warn("agent schedule is invalid — it will never fire", "agent", a.Name, "when", a.When)
			continue
		}
		t, ok := nextAfter(a.When, now)
		if !ok {
			s.log.Warn("agent schedule can never occur — it will never fire", "agent", a.Name, "when", a.When)
			continue
		}
		due[a.Name] = t
	}
	s.log.Info("scheduler started", "scheduled_agents", len(due))
	// Nothing scheduled: no timer at all. A workspace without cron agents must not wake once a
	// minute forever.
	if len(due) == 0 {
		return
	}

	for {
		next := earliest(due)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		s.fireDue(ctx, agents, due, time.Now())
		if len(due) == 0 {
			return // every remaining schedule turned out to be unsatisfiable
		}
	}
}

// fireDue fires every agent in due whose time has come and re-arms it from now, dropping any whose
// spec no longer resolves. A firing later than catchUpGrace is skipped rather than run at the wrong
// time. Each fire runs in its own goroutine so a slow run (fire blocks for a whole turn) never stalls
// the loop; fire honors ctx and exits when Start returns.
func (s *Scheduler) fireDue(ctx context.Context, agents []Agent, due map[string]time.Time, now time.Time) {
	for _, a := range agents {
		at, ok := due[a.Name]
		if !ok || at.After(now) {
			continue
		}
		if late := now.Sub(at); late > catchUpGrace {
			s.log.Warn("scheduled firing missed — outside the catch-up window",
				"agent", a.Name, "when", a.When, "due", at.Format(time.RFC3339), "late", late.Round(time.Second))
		} else {
			s.log.Info("agent fired", "agent", a.Name, "when", a.When, "due", at.Format(time.RFC3339),
				"late", late.Round(time.Second))
			go s.fire(ctx, a)
		}
		next, ok := nextAfter(a.When, now)
		// A DST fall-back replays an hour, so the next occurrence can be the SAME wall-clock minute
		// at a later absolute time — which would run a daily agent twice that day. Step past it.
		if ok && sameWallMinute(next, at) {
			next, ok = nextAfter(a.When, next)
		}
		if !ok {
			// Only reachable if the spec has no occurrence within maxScanYears of now; an agent that
			// can never fire again must not hold the loop open.
			delete(due, a.Name)
			continue
		}
		due[a.Name] = next
	}
}

// sameWallMinute reports whether a and b denote the same local wall-clock minute. For two times the
// solver produced in sequence this happens only across a DST fall-back, where an hour is replayed.
func sameWallMinute(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd && a.Hour() == b.Hour() && a.Minute() == b.Minute()
}

// earliest returns the soonest time in due. It is only called with a non-empty map.
func earliest(due map[string]time.Time) time.Time {
	var first time.Time
	for _, t := range due {
		if first.IsZero() || t.Before(first) {
			first = t
		}
	}
	return first
}

// nextAfter returns the earliest minute strictly after t whose fields match spec. It steps field by
// field (month, then day, then hour, then minute) rather than minute by minute, so a sparse schedule
// resolves in a few hundred iterations instead of millions. ok is false for a malformed spec or one
// that can never occur within maxScanYears.
//
// It resolves in t's own Location, so a spec means local wall-clock time. It reuses fieldMatch, so
// there is only ever one definition of what a spec means — including the plain AND of day-of-month
// and day-of-week (not the OR that system cron uses).
func nextAfter(spec string, t time.Time) (time.Time, bool) {
	f := strings.Fields(spec)
	if len(f) != 5 {
		return time.Time{}, false
	}
	// Start at the next whole minute: a spec matching t exactly must not resolve to t again.
	c := t.Truncate(time.Minute).Add(time.Minute)
	limit := c.AddDate(maxScanYears, 0, 0)
	for c.Before(limit) {
		switch {
		case !fieldMatch(f[3], int(c.Month()), 1, 12):
			c = time.Date(c.Year(), c.Month(), 1, 0, 0, 0, 0, c.Location()).AddDate(0, 1, 0)
		case !fieldMatch(f[2], c.Day(), 1, 31) || !fieldMatch(f[4], int(c.Weekday()), 0, 6):
			c = time.Date(c.Year(), c.Month(), c.Day(), 0, 0, 0, 0, c.Location()).AddDate(0, 0, 1)
		case !fieldMatch(f[1], c.Hour(), 0, 23):
			c = time.Date(c.Year(), c.Month(), c.Day(), c.Hour(), 0, 0, 0, c.Location()).Add(time.Hour)
		case !fieldMatch(f[0], c.Minute(), 0, 59):
			c = c.Add(time.Minute)
		default:
			return c, true
		}
	}
	return time.Time{}, false
}

// validCron reports whether spec is a well-formed 5-field cron (so an invalid one can be flagged
// rather than silently never matching). It mirrors fieldMatch's grammar without a value to match.
func validCron(spec string) bool {
	f := strings.Fields(spec)
	if len(f) != 5 {
		return false
	}
	bounds := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}
	for i, field := range f {
		for part := range strings.SplitSeq(field, ",") {
			if !validPart(part, bounds[i][0], bounds[i][1]) {
				return false
			}
		}
	}
	return true
}

// validPart reports whether one cron part ("*", "n", "a-b", any with "/step") is well-formed within
// [lo,hi].
func validPart(part string, lo, hi int) bool {
	if base, stepStr, ok := strings.Cut(part, "/"); ok {
		if s, err := strconv.Atoi(stepStr); err != nil || s <= 0 {
			return false
		}
		part = base
	}
	switch {
	case part == "*":
		return true
	case strings.ContainsRune(part, '-'):
		a, b, ok := strings.Cut(part, "-")
		x, e1 := strconv.Atoi(a)
		y, e2 := strconv.Atoi(b)
		return ok && e1 == nil && e2 == nil && x >= lo && y <= hi && x <= y
	default:
		n, err := strconv.Atoi(part)
		return err == nil && n >= lo && n <= hi
	}
}

// fieldMatch matches one cron field (a comma list of parts) against val within [lo,hi]. The 5-field
// grammar it serves is: minute hour day-of-month month day-of-week. Each field supports "*", a
// number, "a-b" ranges, "*/n" or "a-b/n" steps, and comma-separated lists of those. Day-of-week is
// 0-6 with Sunday=0.
func fieldMatch(field string, val, lo, hi int) bool {
	for part := range strings.SplitSeq(field, ",") {
		if partMatch(part, val, lo, hi) {
			return true
		}
	}
	return false
}

// partMatch matches one cron part: "*", "n", "a-b", or any of those with a "/step" suffix, within
// the field's [lo,hi] bounds.
func partMatch(part string, val, lo, hi int) bool {
	step := 1
	if base, stepStr, ok := strings.Cut(part, "/"); ok {
		s, err := strconv.Atoi(stepStr)
		if err != nil || s <= 0 {
			return false
		}
		step = s
		part = base
	}

	from, to := lo, hi
	switch {
	case part == "*":
		// full range
	case strings.ContainsRune(part, '-'):
		a, b, ok := strings.Cut(part, "-")
		fromN, err1 := strconv.Atoi(a)
		toN, err2 := strconv.Atoi(b)
		if !ok || err1 != nil || err2 != nil {
			return false
		}
		from, to = fromN, toN
	default:
		n, err := strconv.Atoi(part)
		if err != nil {
			return false
		}
		from, to = n, n
	}

	if val < from || val > to {
		return false
	}
	return (val-from)%step == 0
}
