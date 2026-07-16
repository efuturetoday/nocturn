package agent

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
)

// Scheduler fires cron-triggered agents unattended. It grants NO authority — it only
// picks the moment and stamps the run's autonomy level; every effect still goes
// through the broker + HITL exactly as a manual run. Two guarantees matter:
//
//   - Overlap prevention: at most ONE run of a given agent at a time. A firing while
//     that agent is still running (including one paused on a guarded out-of-band
//     approval) is SKIPPED and logged — never queued, never run in parallel. Same-
//     agent parallelism would race on its /work state and its per-owner grant store;
//     different agents run concurrently (they are isolated).
//   - Autonomy: each firing stamps the agent's declared level (capability.WithAutonomy)
//     so an Ask with no human present resolves per the dial (guarded/strict/full).
type Scheduler struct {
	run   func(ctx context.Context, def Definition) error // runs one firing (caller wires RunTask)
	clock func() time.Time
	log   func(string)

	jobs []job

	mu       sync.Mutex
	inFlight map[string]bool
}

type job struct {
	def      Definition
	schedule Schedule
	expr     string
}

// SchedulerOption configures a Scheduler.
type SchedulerOption func(*Scheduler)

// WithClock injects the time source (for deterministic tests). Default time.Now.
func WithClock(clock func() time.Time) SchedulerOption {
	return func(s *Scheduler) { s.clock = clock }
}

// WithLog sets a line logger for firings, skips and results. Default no-op.
func WithLog(log func(string)) SchedulerOption {
	return func(s *Scheduler) { s.log = log }
}

// NewScheduler parses the cron trigger of every agent whose When is cron("…") and
// builds a scheduler that fires them via run. Agents with a non-cron trigger
// (manual/webhook) are ignored. A malformed cron expression is a hard error surfaced
// HERE, at startup — the operator never gets a job that silently never fires (or
// fires wrong). run is how one firing executes (the caller wires RunTask + the
// agent's own grant store + the scheduled task).
func NewScheduler(defs []Definition, run func(ctx context.Context, def Definition) error, opts ...SchedulerOption) (*Scheduler, error) {
	s := &Scheduler{run: run, clock: time.Now, log: func(string) {}, inFlight: map[string]bool{}}
	for _, o := range opts {
		o(s)
	}
	for _, def := range defs {
		expr, ok := cronExpr(def.When)
		if !ok {
			continue // manual/webhook — not scheduled here
		}
		sched, err := ParseCron(expr)
		if err != nil {
			return nil, fmt.Errorf("agent %q: %w", def.Name, err)
		}
		s.jobs = append(s.jobs, job{def: def, schedule: sched, expr: expr})
	}
	return s, nil
}

// Scheduled returns "name @ <cron>" for every scheduled agent, sorted — for a
// startup notice.
func (s *Scheduler) Scheduled() []string {
	out := make([]string, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, fmt.Sprintf("%s @ %s (autonomy=%s)", j.def.Name, j.expr, autonomyName(j.def.Autonomy)))
	}
	sort.Strings(out)
	return out
}

// Start runs the tick loop until ctx is cancelled. It aligns to each minute boundary
// and calls tick once per minute. Cancelling ctx stops the loop AND (via the derived
// context) any in-flight run.
func (s *Scheduler) Start(ctx context.Context) {
	if len(s.jobs) == 0 {
		return
	}
	go func() {
		for {
			now := s.clock()
			next := now.Truncate(time.Minute).Add(time.Minute)
			timer := time.NewTimer(next.Sub(now))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			s.tick(ctx, next)
		}
	}()
}

// tick fires every job whose schedule matches the given minute, honoring overlap
// prevention. It is pure w.r.t. time (the caller supplies now) so tests drive it
// directly without real clocks.
func (s *Scheduler) tick(ctx context.Context, now time.Time) {
	for _, j := range s.jobs {
		if !j.schedule.Matches(now) {
			continue
		}
		s.mu.Lock()
		if s.inFlight[j.def.Name] {
			s.mu.Unlock()
			s.log(fmt.Sprintf("scheduler: %s still running — skipping this firing", j.def.Name))
			continue
		}
		s.inFlight[j.def.Name] = true
		s.mu.Unlock()
		go s.fire(ctx, j.def)
	}
}

// fire runs one firing to completion, then clears the in-flight mark so the next
// scheduled minute can fire again. Autonomy is stamped here — this is the sole
// unattended entry point. The in-flight mark is cleared BEFORE the result is logged,
// so once "done"/"failed" is observed the agent is free to fire again (no race).
func (s *Scheduler) fire(ctx context.Context, def Definition) {
	runCtx := capability.WithAutonomy(ctx, def.Autonomy)
	s.log(fmt.Sprintf("scheduler: firing %s (autonomy=%s)", def.Name, autonomyName(def.Autonomy)))
	err := s.run(runCtx, def)

	s.mu.Lock()
	delete(s.inFlight, def.Name)
	s.mu.Unlock()

	if err != nil {
		s.log(fmt.Sprintf("scheduler: %s failed: %v", def.Name, err))
		return
	}
	s.log(fmt.Sprintf("scheduler: %s done", def.Name))
}

func autonomyName(a capability.Autonomy) string {
	switch a {
	case capability.AutonomyStrict:
		return "strict"
	case capability.AutonomyFull:
		return "full"
	case capability.AutonomyGuarded:
		return "guarded"
	default:
		return "attended"
	}
}
