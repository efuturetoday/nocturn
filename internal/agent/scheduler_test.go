package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
)

// waitForLog drains the log channel until a line contains substr, failing on timeout
// rather than hanging.
func waitForLog(t *testing.T, logs <-chan string, substr string) {
	t.Helper()
	for {
		select {
		case m := <-logs:
			if strings.Contains(m, substr) {
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for a log containing %q", substr)
		}
	}
}

func TestScheduler_TickFiresMatchWithAutonomy(t *testing.T) {
	defs := []Definition{
		{Name: "a", When: `cron("0 7 * * *")`, Autonomy: capability.AutonomyGuarded, Tools: []string{"file"}},
		{Name: "b", When: `cron("0 8 * * *")`, Autonomy: capability.AutonomyStrict, Tools: []string{"file"}},
	}
	type firing struct {
		name string
		auto capability.Autonomy
	}
	fired := make(chan firing, 4)
	run := func(ctx context.Context, def Definition) error {
		fired <- firing{def.Name, capability.AutonomyFrom(ctx)}
		return nil
	}
	s, err := NewScheduler(defs, run)
	if err != nil {
		t.Fatal(err)
	}

	s.tick(context.Background(), at(2026, 7, 15, 7, 0)) // matches "a" only
	got := <-fired
	if got.name != "a" || got.auto != capability.AutonomyGuarded {
		t.Fatalf("fired %+v, want a/guarded", got)
	}
	select {
	case g := <-fired:
		t.Fatalf("unexpected extra firing %+v (only a matches 07:00)", g)
	default:
	}
}

// Overlap prevention: a firing while the agent is still running is skipped, not
// queued or parallelized; once it completes, the next firing runs again.
func TestScheduler_SkipsOverlap(t *testing.T) {
	def := Definition{Name: "x", When: `cron("* * * * *")`, Autonomy: capability.AutonomyGuarded, Tools: []string{"file"}}
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	var runs int32
	run := func(ctx context.Context, _ Definition) error {
		atomic.AddInt32(&runs, 1)
		started <- struct{}{}
		<-release
		return nil
	}
	logs := make(chan string, 32)
	s, err := NewScheduler([]Definition{def}, run, WithLog(func(m string) { logs <- m }))
	if err != nil {
		t.Fatal(err)
	}
	now := at(2026, 7, 15, 7, 0) // "* * * * *" matches every minute

	s.tick(context.Background(), now)
	<-started // run #1 is in flight, blocked on release

	// A second firing while #1 runs → skipped, no new run.
	s.tick(context.Background(), now)
	waitForLog(t, logs, "skipping")
	if n := atomic.LoadInt32(&runs); n != 1 {
		t.Fatalf("overlap: run invoked %d times, want 1", n)
	}

	// Release #1 → it completes and the in-flight mark clears (before the done log).
	close(release)
	waitForLog(t, logs, "x done")

	// Now the agent is free: the next firing runs again.
	s.tick(context.Background(), now)
	<-started
	if n := atomic.LoadInt32(&runs); n != 2 {
		t.Fatalf("after completion run invoked %d times, want 2", n)
	}
}

func TestNewScheduler_BadCronFailsClosed(t *testing.T) {
	run := func(context.Context, Definition) error { return nil }
	_, err := NewScheduler([]Definition{{Name: "bad", When: `cron("not a cron")`}}, run)
	if err == nil {
		t.Fatal("a malformed cron trigger must fail at NewScheduler")
	}
}

func TestNewScheduler_IgnoresNonCron(t *testing.T) {
	run := func(context.Context, Definition) error { return nil }
	s, err := NewScheduler([]Definition{
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
