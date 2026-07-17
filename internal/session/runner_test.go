package session_test

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/efuturetoday/nocturn/internal/activity"
	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/session"
)

// fakeTurns is a test double for the turns a Runner drives (Session in production):
// it delegates each turn to ask and counts resets, so the queue/loop is exercised
// without a real model or guard.
type fakeTurns struct {
	ask    func(context.Context, string) (string, error)
	mu     sync.Mutex
	resets int
}

func (f *fakeTurns) Ask(ctx context.Context, input string) (string, error) { return f.ask(ctx, input) }
func (f *fakeTurns) Reset() {
	f.mu.Lock()
	f.resets++
	f.mu.Unlock()
}
func (f *fakeTurns) History() []brain.Message { return nil }
func (f *fakeTurns) resetCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resets
}

// recv reads one event with a timeout so a stuck runner fails instead of hanging.
func recv(t *testing.T, sub <-chan session.Event) session.Event {
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

func startRunner(t *testing.T, run func(context.Context, string) (string, error)) (*session.Runner, <-chan session.Event) {
	r := session.NewRunner(&fakeTurns{ask: run})
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

	r.Submit(session.SourceUser, "a")
	if e, ok := recv(t, sub).(session.TurnStartEvent); !ok || e.Input != "a" {
		t.Fatalf("want TurnStart(a), got %#v", e)
	}

	// b and c arrive while "a" runs → both buffered.
	r.Submit(session.SourceUser, "b")
	r.Submit(session.SourceWake, "c")
	if e, ok := recv(t, sub).(session.QueuedEvent); !ok || e.Input != "b" {
		t.Fatalf("want Queued(b), got %#v", e)
	}
	if e, ok := recv(t, sub).(session.QueuedEvent); !ok || e.Input != "c" || e.Source != session.SourceWake {
		t.Fatalf("want Queued(c, wake), got %#v", e)
	}

	if s := r.Snapshot(); !s.Running || len(s.Queue) != 2 {
		t.Fatalf("snapshot = running:%v queue:%d, want running/2", s.Running, len(s.Queue))
	}

	// Release "a" → it ends, "b" starts; release "b" → "c" (a wake) starts.
	g.release <- struct{}{}
	mustTurnEnd(t, sub, "a")
	mustTurnStart(t, sub, "b", session.SourceUser)
	g.release <- struct{}{}
	mustTurnEnd(t, sub, "b")
	mustTurnStart(t, sub, "c", session.SourceWake)
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

	r.Submit(session.SourceUser, "x")
	if _, ok := recv(t, sub1).(session.TurnStartEvent); !ok {
		t.Fatal("sub1 missed TurnStart")
	}
	if _, ok := recv(t, sub2).(session.TurnStartEvent); !ok {
		t.Fatal("sub2 missed TurnStart")
	}
}

// A turn's activity (a token emitted to the ctx sink the Runner installs) fans out
// as a TokenEvent — the single stream sink replaces the old TokenSink wiring.
func TestRunner_StreamsTokenFromTurn(t *testing.T) {
	run := func(ctx context.Context, _ string) (string, error) {
		activity.Emit(ctx, activity.Token{Text: "hello"})
		return "done", nil
	}
	r, sub := startRunner(t, run)
	r.Submit(session.SourceUser, "go")

	// TurnStart, then the streamed token, then TurnEnd.
	if _, ok := recv(t, sub).(session.TurnStartEvent); !ok {
		t.Fatal("want TurnStart")
	}
	if e, ok := recv(t, sub).(session.TokenEvent); !ok || e.Text != "hello" {
		t.Fatalf("want TokenEvent(hello), got %#v", e)
	}
}

// Reset drops the queue and starts fresh, even mid-turn. Under synctest the async
// unwind of the cancelled turn is awaited deterministically with synctest.Wait, so no
// polling sleep is needed (go.dev/blog/testing-time).
func TestRunner_Reset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newGatedRun()
		ft := &fakeTurns{ask: g.fn}
		r := session.NewRunner(ft)
		sub, unsub := r.Subscribe()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(func() { unsub(); cancel() })
		r.Start(ctx)

		r.Submit(session.SourceUser, "a")
		mustTurnStart(t, sub, "a", session.SourceUser)
		r.Submit(session.SourceUser, "queued")
		if _, ok := recv(t, sub).(session.QueuedEvent); !ok {
			t.Fatal("want Queued")
		}

		r.Reset() // cancels the running turn, drops the queue, resets the session

		// Drain events until the notice (the cancelled turn may emit others first).
		for {
			if _, ok := recv(t, sub).(session.NoticeEvent); ok {
				break
			}
		}
		// Wait for the cancelled turn to fully unwind, then assert idle + empty.
		synctest.Wait()
		if s := r.Snapshot(); s.Running || len(s.Queue) != 0 {
			t.Fatalf("runner not idle/empty after Reset: running=%v queue=%d", s.Running, len(s.Queue))
		}
		if rc := ft.resetCount(); rc != 1 {
			t.Fatalf("reset called %d times, want 1", rc)
		}
	})
}

// An approval surfaces as an event; Resolve runs the chosen option's apply callback
// and clears the pending state. A stale Resolve is a no-op. The engine holds no
// approval-mechanism types — apply is opaque.
func TestRunner_Approval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var applied []int
		r := session.NewRunner(&fakeTurns{ask: func(context.Context, string) (string, error) { return "", nil }})
		sub, unsub := r.Subscribe()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(func() { unsub(); cancel() })
		r.Start(ctx)

		// What the attended notifier does during a turn: show labels + supply apply(choice)
		// (which, in production, resolves the chosen hitl token).
		apply := func(choice int) { mu.Lock(); applied = append(applied, choice); mu.Unlock() }
		r.PresentApproval("Send email to x@a", []string{"Allow once", "Deny"}, apply)

		ev, ok := recv(t, sub).(session.ApprovalEvent)
		if !ok || ev.Intent != "Send email to x@a" || len(ev.Options) != 2 || ev.Options[0] != "Allow once" {
			t.Fatalf("want ApprovalEvent, got %#v", ev)
		}
		if s := r.Snapshot(); s.Pending == nil || s.Pending.ID != ev.ID {
			t.Fatalf("snapshot pending = %#v", s.Pending)
		}

		r.Resolve(ev.ID, 0) // "Allow once"
		if e, ok := recv(t, sub).(session.ApprovalResolvedEvent); !ok || e.ID != ev.ID {
			t.Fatalf("want ApprovalResolvedEvent, got %#v", e)
		}
		mu.Lock()
		got := append([]int(nil), applied...)
		mu.Unlock()
		if len(got) != 1 || got[0] != 0 {
			t.Fatalf("apply got %v, want [0]", got)
		}
		if s := r.Snapshot(); s.Pending != nil {
			t.Fatal("pending not cleared after resolve")
		}

		// A stale resolve (already answered) does nothing. synctest.Wait lets any work it
		// might spawn settle before we assert nothing changed — no arbitrary sleep.
		r.Resolve(ev.ID, 1)
		synctest.Wait()
		mu.Lock()
		n := len(applied)
		mu.Unlock()
		if n != 1 {
			t.Fatalf("stale resolve applied again (%d)", n)
		}
	})
}

// SubmitAgent runs the injected agent runner on the same serialized loop: its Display
// is the client line, its Input is the task, and it streams/gates like a session turn.
func TestRunner_SubmitAgent_RoutesToAgentRunner(t *testing.T) {
	var mu sync.Mutex
	var gotName, gotTask string
	r := session.NewRunner(
		&fakeTurns{ask: func(context.Context, string) (string, error) { return "session", nil }},
		session.WithAgentRunner(func(_ context.Context, name, task string) (string, error) {
			mu.Lock()
			gotName, gotTask = name, task
			mu.Unlock()
			return "agent-answer", nil
		}),
	)
	sub, unsub := r.Subscribe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { unsub(); cancel() })
	r.Start(ctx)

	r.SubmitAgent("/researcher dig in", "researcher", "dig in")
	e, ok := recv(t, sub).(session.TurnStartEvent)
	if !ok || e.Source != session.SourceAgent || e.Display != "/researcher dig in" || e.Input != "dig in" {
		t.Fatalf("want TurnStart(agent, display, task), got %#v", e)
	}
	mustTurnEnd(t, sub, "agent-answer")
	mu.Lock()
	defer mu.Unlock()
	if gotName != "researcher" || gotTask != "dig in" {
		t.Fatalf("agent runner saw (%q,%q), want (researcher,dig in)", gotName, gotTask)
	}
}

// A SubmitAgent with no agent runner wired fails the turn (an error TurnEnd), never panics.
func TestRunner_SubmitAgent_NoRunner_ErrorsTurn(t *testing.T) {
	r := session.NewRunner(&fakeTurns{ask: func(context.Context, string) (string, error) { return "", nil }})
	sub, unsub := r.Subscribe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { unsub(); cancel() })
	r.Start(ctx)

	r.SubmitAgent("/x", "x", "go")
	if _, ok := recv(t, sub).(session.TurnStartEvent); !ok {
		t.Fatal("want TurnStart")
	}
	if e, ok := recv(t, sub).(session.TurnEndEvent); !ok || e.Err == nil {
		t.Fatalf("want TurnEnd with an error, got %#v", e)
	}
}

// A turn on an UNWATCHED runner (no subscribers) carries NO approval sink, so the
// gateway falls back to out-of-band instead of parking the approval on a stream no one
// sees. A watched turn carries the sink so approvals surface attended.
func TestRunner_ApprovalSink_OnlyWhenWatched(t *testing.T) {
	sinkPresence := func(subscribe bool) bool {
		got := make(chan bool, 1)
		run := func(ctx context.Context, _ string) (string, error) {
			got <- session.ApprovalSinkFrom(ctx) != nil
			return "", nil
		}
		r := session.NewRunner(&fakeTurns{ask: run})
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		if subscribe {
			_, unsub := r.Subscribe()
			t.Cleanup(unsub)
		}
		r.Start(ctx)
		r.Submit(session.SourceUser, "go")
		select {
		case has := <-got:
			return has
		case <-time.After(2 * time.Second):
			t.Fatal("turn did not run")
			return false
		}
	}

	if sinkPresence(false) {
		t.Error("unwatched turn carried an approval sink; want none (falls back to out-of-band)")
	}
	if !sinkPresence(true) {
		t.Error("watched turn missing its approval sink; approvals could not surface attended")
	}
}

func mustTurnStart(t *testing.T, sub <-chan session.Event, input string, src session.Source) {
	t.Helper()
	e, ok := recv(t, sub).(session.TurnStartEvent)
	if !ok || e.Input != input || e.Source != src {
		t.Fatalf("want TurnStart(%s,%s), got %#v", input, src, e)
	}
}

func mustTurnEnd(t *testing.T, sub <-chan session.Event, answer string) {
	t.Helper()
	e, ok := recv(t, sub).(session.TurnEndEvent)
	if !ok || e.Answer != answer {
		t.Fatalf("want TurnEnd(%s), got %#v", answer, e)
	}
}
