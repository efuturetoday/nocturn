package agent

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
)

// Scheduler fires cron-triggered agents unattended. It grants NO authority and does ONE
// thing: pick the moment. A firing runs as a fresh one-shot chat whose charter carries
// the agent's declared autonomy dial, and every effect still goes through the broker +
// HITL exactly as a manual run — the scheduler stamps nothing on the run.
//
// Overlap prevention (at most ONE run of a given agent at a time — same-agent parallelism
// would race on its per-owner grant store and working state) lives in the run target, not
// here: a firing while that agent is still running returns an error, which the scheduler
// logs and drops. Different agents run concurrently (they are isolated).
type Scheduler struct {
	run func(ctx context.Context, def Agent) error // runs one firing (caller wires the one-shot fire)
	log func(string)

	jobs []job
}

type job struct {
	def      Agent
	schedule Schedule
	expr     string
}

// SchedulerOption configures a Scheduler.
type SchedulerOption func(*Scheduler)

// WithLog sets a line logger for firings, skips and results. Default no-op.
func WithLog(log func(string)) SchedulerOption {
	return func(s *Scheduler) { s.log = log }
}

// NewScheduler parses the cron trigger of every agent whose When is cron("…") and
// builds a scheduler that fires them via run. Agents with a non-cron trigger
// (manual/webhook) are ignored. A malformed cron expression is a hard error surfaced
// HERE, at startup — the operator never gets a job that silently never fires (or
// fires wrong). run is how one firing executes (the caller wires Run + the
// agent's own grant store + the scheduled task).
func NewScheduler(defs []Agent, run func(ctx context.Context, def Agent) error, opts ...SchedulerOption) (*Scheduler, error) {
	s := &Scheduler{run: run, log: func(string) {}}
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
			now := time.Now()
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

// tick fires every job whose schedule matches the given minute; overlap prevention is the
// target's concern (a busy agent's firing returns an error, logged in fire). It is pure
// w.r.t. time (the caller supplies now) so tests drive it directly without real clocks.
func (s *Scheduler) tick(ctx context.Context, now time.Time) {
	for _, j := range s.jobs {
		if j.schedule.Matches(now) {
			s.log(fmt.Sprintf("scheduler: firing %s", j.def.Name))
			go s.fire(ctx, j.def)
		}
	}
}

// fire runs one firing via the wired target and logs a non-nil result — the target's own
// error, or an overlap skip when that agent is already running. Autonomy and overlap both
// belong to the target (the one-shot chat + its charter); the scheduler stamps nothing.
func (s *Scheduler) fire(ctx context.Context, def Agent) {
	if err := s.run(ctx, def); err != nil {
		s.log(fmt.Sprintf("scheduler: %s: %v", def.Name, err))
	}
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
