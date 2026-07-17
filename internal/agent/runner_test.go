package agent_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/agent"
)

// recv reads one event with a timeout so a stuck runner fails instead of hanging.
func recv(t *testing.T, sub <-chan agent.Event) agent.Event {
	t.Helper()
	select {
	case e := <-sub:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an event")
		return nil
	}
}

// gatedRun blocks each turn on a release channel so a test can drive turn timing; it
// also returns on ctx cancellation (so Cancel/Reset can interrupt a turn).
type gatedRun struct {
	mu      sync.Mutex
	started []string
	release chan struct{}
}

func newGatedRun() *gatedRun { return &gatedRun{release: make(chan struct{})} }

func (g *gatedRun) fn(ctx context.Context, input string) (string, error) {
	g.mu.Lock()
	g.started = append(g.started, input)
	g.mu.Unlock()
	select {
	case <-g.release:
		return input, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func startRunner(t *testing.T, run func(context.Context, string) (string, error)) (*agent.Runner, <-chan agent.Event) {
	r := agent.NewRunner(run, nil, nil)
	sub, unsub := r.Subscribe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { unsub(); cancel() })
	r.Start(ctx)
	return r, sub
}

// A turn runs immediately; inputs submitted while it runs are buffered and fed FIFO
// when it ends. Sources are preserved (a wake note vs a user message).
func TestRunner_BuffersAndDrainsFIFO(t *testing.T) {
	g := newGatedRun()
	r, sub := startRunner(t, g.fn)

	r.Submit(agent.SourceUser, "a")
	if e, ok := recv(t, sub).(agent.TurnStartEvent); !ok || e.Input != "a" {
		t.Fatalf("want TurnStart(a), got %#v", e)
	}

	// b and c arrive while "a" runs → both buffered.
	r.Submit(agent.SourceUser, "b")
	r.Submit(agent.SourceWake, "c")
	if e, ok := recv(t, sub).(agent.QueuedEvent); !ok || e.Input != "b" {
		t.Fatalf("want Queued(b), got %#v", e)
	}
	if e, ok := recv(t, sub).(agent.QueuedEvent); !ok || e.Input != "c" || e.Source != agent.SourceWake {
		t.Fatalf("want Queued(c, wake), got %#v", e)
	}

	if s := r.Snapshot(); !s.Running || len(s.Queue) != 2 {
		t.Fatalf("snapshot = running:%v queue:%d, want running/2", s.Running, len(s.Queue))
	}

	// Release "a" → it ends, "b" starts; release "b" → "c" (a wake) starts.
	g.release <- struct{}{}
	mustTurnEnd(t, sub, "a")
	mustTurnStart(t, sub, "b", agent.SourceUser)
	g.release <- struct{}{}
	mustTurnEnd(t, sub, "b")
	mustTurnStart(t, sub, "c", agent.SourceWake)
	g.release <- struct{}{}
	mustTurnEnd(t, sub, "c")

	if s := r.Snapshot(); s.Running || len(s.Queue) != 0 {
		t.Fatalf("after drain: running:%v queue:%d, want idle/empty", s.Running, len(s.Queue))
	}
}

// Every subscriber sees every event (fan-out).
func TestRunner_FanOut(t *testing.T) {
	g := newGatedRun()
	r, sub1 := startRunner(t, g.fn)
	sub2, unsub2 := r.Subscribe()
	defer unsub2()

	r.Submit(agent.SourceUser, "x")
	if _, ok := recv(t, sub1).(agent.TurnStartEvent); !ok {
		t.Fatal("sub1 missed TurnStart")
	}
	if _, ok := recv(t, sub2).(agent.TurnStartEvent); !ok {
		t.Fatal("sub2 missed TurnStart")
	}
}

// TokenSink streams tokens out as events.
func TestRunner_TokenSink(t *testing.T) {
	r := agent.NewRunner(func(context.Context, string) (string, error) { return "", nil }, nil, nil)
	sub, unsub := r.Subscribe()
	defer unsub()
	r.TokenSink()("hello")
	if e, ok := recv(t, sub).(agent.TokenEvent); !ok || e.Text != "hello" {
		t.Fatalf("want TokenEvent(hello), got %#v", e)
	}
}

// Reset drops the queue and starts fresh, even mid-turn.
func TestRunner_Reset(t *testing.T) {
	g := newGatedRun()
	resetCalls := 0
	var mu sync.Mutex
	r := agent.NewRunner(g.fn, func() { mu.Lock(); resetCalls++; mu.Unlock() }, nil)
	sub, unsub := r.Subscribe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { unsub(); cancel() })
	r.Start(ctx)

	r.Submit(agent.SourceUser, "a")
	mustTurnStart(t, sub, "a", agent.SourceUser)
	r.Submit(agent.SourceUser, "queued")
	if _, ok := recv(t, sub).(agent.QueuedEvent); !ok {
		t.Fatal("want Queued")
	}

	r.Reset() // cancels the running turn, drops the queue, resets the session

	// We should see a notice and, eventually, an idle empty runner.
	sawNotice := false
	deadline := time.After(2 * time.Second)
	for !sawNotice {
		select {
		case e := <-sub:
			if _, ok := e.(agent.NoticeEvent); ok {
				sawNotice = true
			}
		case <-deadline:
			t.Fatal("no NoticeEvent after Reset")
		}
	}
	// Give the cancelled turn a moment to unwind, then assert idle+empty.
	for range 50 {
		if s := r.Snapshot(); !s.Running && len(s.Queue) == 0 {
			mu.Lock()
			rc := resetCalls
			mu.Unlock()
			if rc != 1 {
				t.Fatalf("reset called %d times, want 1", rc)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("runner not idle/empty after Reset")
}

func mustTurnStart(t *testing.T, sub <-chan agent.Event, input string, src agent.Source) {
	t.Helper()
	e, ok := recv(t, sub).(agent.TurnStartEvent)
	if !ok || e.Input != input || e.Source != src {
		t.Fatalf("want TurnStart(%s,%s), got %#v", input, src, e)
	}
}

func mustTurnEnd(t *testing.T, sub <-chan agent.Event, answer string) {
	t.Helper()
	e, ok := recv(t, sub).(agent.TurnEndEvent)
	if !ok || e.Answer != answer {
		t.Fatalf("want TurnEnd(%s), got %#v", answer, e)
	}
}
