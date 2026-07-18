package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/efuturetoday/nocturn/internal/capability"
)

// waitForLog drains the log channel until a line contains substr. Callers run inside a
// synctest bubble, so a log that never arrives surfaces as an instant deadlock panic
// (with a stack) rather than a real-time timeout (go.dev/blog/testing-time).
func waitForLog(t *testing.T, logs <-chan string, substr string) {
	t.Helper()
	for m := range logs {
		if strings.Contains(m, substr) {
			return
		}
	}
}

// The scheduler fires exactly the jobs whose cron matches the minute — nothing else. It
// stamps NO autonomy (that is the one-shot chat's charter now); it only picks the moment.
func TestScheduler_TickFiresMatch(t *testing.T) {
	defs := []Agent{
		{Name: "a", When: `cron("0 7 * * *")`, Autonomy: capability.AutonomyGuarded, Tools: []string{"file"}},
		{Name: "b", When: `cron("0 8 * * *")`, Autonomy: capability.AutonomyStrict, Tools: []string{"file"}},
	}
	fired := make(chan string, 4)
	run := func(ctx context.Context, def Agent) error {
		fired <- def.Name
		return nil
	}
	s, err := NewScheduler(defs, run)
	if err != nil {
		t.Fatal(err)
	}

	s.tick(context.Background(), at(2026, 7, 15, 7, 0)) // matches "a" only
	if got := <-fired; got != "a" {
		t.Fatalf("fired %q, want a", got)
	}
	select {
	case g := <-fired:
		t.Fatalf("unexpected extra firing %q (only a matches 07:00)", g)
	default:
	}
}

// Overlap prevention no longer lives in the scheduler — it fires EVERY matching minute and
// delegates to the target, surfacing whatever error the target returns (e.g. the one-shot
// manager's "agent still running" skip). The scheduler itself stamps and guards nothing.
func TestScheduler_FiresEveryMatch_DelegatesOverlap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		def := Agent{Name: "x", When: `cron("* * * * *")`, Autonomy: capability.AutonomyGuarded, Tools: []string{"file"}}
		var runs atomic.Int32
		run := func(ctx context.Context, _ Agent) error {
			runs.Add(1)
			return errors.New("agent still running — firing skipped") // the target's own overlap guard
		}
		logs := make(chan string, 32)
		s, err := NewScheduler([]Agent{def}, run, WithLog(func(m string) { logs <- m }))
		if err != nil {
			t.Fatal(err)
		}
		now := at(2026, 7, 15, 7, 0) // "* * * * *" matches every minute

		s.tick(context.Background(), now)
		s.tick(context.Background(), now) // a second firing is NOT self-skipped
		waitForLog(t, logs, "still running")
		synctest.Wait()
		if n := runs.Load(); n != 2 {
			t.Fatalf("run invoked %d times, want 2 — the scheduler fires every match and delegates overlap", n)
		}
	})
}

func TestNewScheduler_BadCronFailsClosed(t *testing.T) {
	run := func(context.Context, Agent) error { return nil }
	_, err := NewScheduler([]Agent{{Name: "bad", When: `cron("not a cron")`}}, run)
	if err == nil {
		t.Fatal("a malformed cron trigger must fail at NewScheduler")
	}
}

func TestNewScheduler_IgnoresNonCron(t *testing.T) {
	run := func(context.Context, Agent) error { return nil }
	s, err := NewScheduler([]Agent{
		{Name: "m", When: "manual"},
		{Name: "w", When: "webhook"},
		{Name: "c", When: `cron("0 7 * * *")`, Autonomy: capability.AutonomyGuarded},
	}, run)
	if err != nil {
		t.Fatal(err)
	}
	sched := s.Scheduled()
	if len(sched) != 1 || !strings.HasPrefix(sched[0], "c @ 0 7 * * *") {
		t.Fatalf("Scheduled() = %v, want only c", sched)
	}
}
