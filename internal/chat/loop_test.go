package chat_test

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/efuturetoday/nocturn/internal/activity"
	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// echoModel answers each turn immediately with the turn's input — the minimal real
// model behind a Chat, so the loop is exercised end to end without an endpoint.
type echoModel struct{}

func (echoModel) Next(_ context.Context, conv []brain.Message, _ []tool.Spec) (brain.Step, error) {
	return brain.Step{Answer: lastUser(conv)}, nil
}

func lastUser(conv []brain.Message) string {
	for i := len(conv) - 1; i >= 0; i-- {
		if conv[i].Role == "user" {
			return conv[i].Content
		}
	}
	return ""
}

// gatedModel blocks each turn on a release channel so a test can drive turn timing;
// it also returns on ctx cancellation (so Cancel/Reset can interrupt a turn). The
// answer echoes the input, so mustTurnEnd can assert which turn finished.
type gatedModel struct {
	release chan struct{}
}

func newGatedModel() *gatedModel { return &gatedModel{release: make(chan struct{})} }

func (g *gatedModel) Next(ctx context.Context, conv []brain.Message, _ []tool.Spec) (brain.Step, error) {
	select {
	case <-g.release:
		return brain.Step{Answer: lastUser(conv)}, nil
	case <-ctx.Done():
		return brain.Step{}, ctx.Err()
	}
}

// modelFunc adapts a function to brain.Model, for one-off scripted behavior.
type modelFunc func(ctx context.Context, conv []brain.Message, specs []tool.Spec) (brain.Step, error)

func (f modelFunc) Next(ctx context.Context, conv []brain.Message, specs []tool.Spec) (brain.Step, error) {
	return f(ctx, conv, specs)
}

// newChat builds an unstarted Chat over model with an empty toolset and no authority —
// the loop-test fixture. Options extend it (agent runner, history).
func newChat(model brain.Model, opts ...chat.Option) *chat.Chat {
	return chat.New(brain.New(model), &gateway.Guard{}, chat.Meta{ID: "c0"},
		chat.Charter{Tools: tool.NewRegistry()}, opts...)
}

// startChat spins a Chat's loop and subscribes to it, cleaning both up with the test.
func startChat(t *testing.T, model brain.Model, opts ...chat.Option) (*chat.Chat, <-chan chat.Event) {
	t.Helper()
	c := newChat(model, opts...)
	sub, unsub := c.Subscribe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { unsub(); cancel() })
	c.Start(ctx)
	return c, sub
}

// recv reads one event with a timeout so a stuck chat fails instead of hanging.
func recv(t *testing.T, sub <-chan chat.Event) chat.Event {
	t.Helper()
	select {
	case e := <-sub:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an event")
		return nil
	}
}

// A turn runs immediately; inputs submitted while it runs are buffered and fed FIFO
// when it ends. Sources are preserved (a wake note vs a user message).
func TestChat_BuffersAndDrainsFIFO(t *testing.T) {
	g := newGatedModel()
	c, sub := startChat(t, g)

	c.Submit(chat.SourceUser, "a")
	if e, ok := recv(t, sub).(chat.TurnStartEvent); !ok || e.Input != "a" {
		t.Fatalf("want TurnStart(a), got %#v", e)
	}

	// b and c arrive while "a" runs → both buffered.
	c.Submit(chat.SourceUser, "b")
	c.Submit(chat.SourceWake, "c")
	if e, ok := recv(t, sub).(chat.QueuedEvent); !ok || e.Input != "b" {
		t.Fatalf("want Queued(b), got %#v", e)
	}
	if e, ok := recv(t, sub).(chat.QueuedEvent); !ok || e.Input != "c" || e.Source != chat.SourceWake {
		t.Fatalf("want Queued(c, wake), got %#v", e)
	}

	if s := c.Snapshot(); !s.Running || len(s.Queue) != 2 {
		t.Fatalf("snapshot = running:%v queue:%d, want running/2", s.Running, len(s.Queue))
	}

	// Release "a" → it ends, "b" starts; release "b" → "c" (a wake) starts.
	g.release <- struct{}{}
	mustTurnEnd(t, sub, "a")
	mustTurnStart(t, sub, "b", chat.SourceUser)
	g.release <- struct{}{}
	mustTurnEnd(t, sub, "b")
	mustTurnStart(t, sub, "c", chat.SourceWake)
	g.release <- struct{}{}
	mustTurnEnd(t, sub, "c")

	if s := c.Snapshot(); s.Running || len(s.Queue) != 0 {
		t.Fatalf("after drain: running:%v queue:%d, want idle/empty", s.Running, len(s.Queue))
	}
}

// Every subscriber sees every event (fan-out).
func TestChat_FanOut(t *testing.T) {
	g := newGatedModel()
	c, sub1 := startChat(t, g)
	sub2, unsub2 := c.Subscribe()
	defer unsub2()

	c.Submit(chat.SourceUser, "x")
	if _, ok := recv(t, sub1).(chat.TurnStartEvent); !ok {
		t.Fatal("sub1 missed TurnStart")
	}
	if _, ok := recv(t, sub2).(chat.TurnStartEvent); !ok {
		t.Fatal("sub2 missed TurnStart")
	}
	g.release <- struct{}{} // let the turn finish so its goroutine exits cleanly
}

// A turn's activity (a token the model emits to the ctx sink the loop installs) fans
// out as a TokenEvent — the single stream sink drives the whole event surface.
func TestChat_StreamsTokenFromTurn(t *testing.T) {
	model := modelFunc(func(ctx context.Context, _ []brain.Message, _ []tool.Spec) (brain.Step, error) {
		activity.Emit(ctx, activity.Token{Text: "hello"})
		return brain.Step{Answer: "done"}, nil
	})
	c, sub := startChat(t, model)
	c.Submit(chat.SourceUser, "go")

	// TurnStart, then the streamed token, then TurnEnd.
	if _, ok := recv(t, sub).(chat.TurnStartEvent); !ok {
		t.Fatal("want TurnStart")
	}
	if e, ok := recv(t, sub).(chat.TokenEvent); !ok || e.Text != "hello" {
		t.Fatalf("want TokenEvent(hello), got %#v", e)
	}
}

// Reset drops the queue, cancels the running turn, and clears the history, even
// mid-turn. Under synctest the async unwind of the cancelled turn is awaited
// deterministically with synctest.Wait, so no polling sleep is needed.
func TestChat_Reset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newGatedModel()
		c := newChat(g)
		sub, unsub := c.Subscribe()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(func() { unsub(); cancel() })
		c.Start(ctx)

		c.Submit(chat.SourceUser, "a")
		mustTurnStart(t, sub, "a", chat.SourceUser)
		c.Submit(chat.SourceUser, "queued")
		if _, ok := recv(t, sub).(chat.QueuedEvent); !ok {
			t.Fatal("want Queued")
		}

		c.Reset() // cancels the running turn, drops the queue, starts a fresh conversation

		// Drain events until the notice (the cancelled turn may emit others first).
		for {
			if _, ok := recv(t, sub).(chat.NoticeEvent); ok {
				break
			}
		}
		// Wait for the cancelled turn to fully unwind, then assert idle + empty + cleared.
		synctest.Wait()
		if s := c.Snapshot(); s.Running || len(s.Queue) != 0 {
			t.Fatalf("chat not idle/empty after Reset: running=%v queue=%d", s.Running, len(s.Queue))
		}
		if msgs := c.Snapshot().Messages; len(msgs) != 0 {
			t.Fatalf("Reset did not clear the history: %+v", msgs)
		}
	})
}

// An approval surfaces as an event; Resolve runs the chosen option's apply callback
// and clears the pending state. A stale Resolve is a no-op. The loop holds no
// approval-mechanism types — apply is opaque.
func TestChat_Approval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var applied []int
		c := newChat(echoModel{})
		sub, unsub := c.Subscribe()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(func() { unsub(); cancel() })
		c.Start(ctx)

		// What the attended notifier does during a turn: show labels + supply apply(choice)
		// (which, in production, resolves the chosen hitl token).
		apply := func(choice int) { mu.Lock(); applied = append(applied, choice); mu.Unlock() }
		c.PresentApproval("Send email to x@a", []string{"Allow once", "Deny"}, apply)

		ev, ok := recv(t, sub).(chat.ApprovalEvent)
		if !ok || ev.Intent != "Send email to x@a" || len(ev.Options) != 2 || ev.Options[0] != "Allow once" {
			t.Fatalf("want ApprovalEvent, got %#v", ev)
		}
		if s := c.Snapshot(); s.Pending == nil || s.Pending.ID != ev.ID {
			t.Fatalf("snapshot pending = %#v", s.Pending)
		}

		c.Resolve(ev.ID, 0) // "Allow once"
		if e, ok := recv(t, sub).(chat.ApprovalResolvedEvent); !ok || e.ID != ev.ID {
			t.Fatalf("want ApprovalResolvedEvent, got %#v", e)
		}
		mu.Lock()
		got := append([]int(nil), applied...)
		mu.Unlock()
		if len(got) != 1 || got[0] != 0 {
			t.Fatalf("apply got %v, want [0]", got)
		}
		if s := c.Snapshot(); s.Pending != nil {
			t.Fatal("pending not cleared after resolve")
		}

		// A stale resolve (already answered) does nothing. synctest.Wait lets any work it
		// might spawn settle before we assert nothing changed — no arbitrary sleep.
		c.Resolve(ev.ID, 1)
		synctest.Wait()
		mu.Lock()
		n := len(applied)
		mu.Unlock()
		if n != 1 {
			t.Fatalf("stale resolve applied again (%d)", n)
		}
	})
}

// SubmitAgent resolves the named agent's charter (WithAgents) and runs it as a
// one-shot Once on the same serialized loop: its Display is the client line, its
// Input is the task, and the child's turn — over its OWN charter (here: its own
// system prompt) — streams and gates like a chat turn. The parent's history is
// untouched: the spawn ran in a throwaway conversation.
func TestChat_SubmitAgent_ResolvesCharterAndRunsOnce(t *testing.T) {
	var mu sync.Mutex
	var gotName string
	c, sub := startChat(t, echoModel{},
		chat.WithAgents(func(name string) (chat.Charter, error) {
			mu.Lock()
			gotName = name
			mu.Unlock()
			return chat.Charter{Tools: tool.NewRegistry(), System: "You are researcher."}, nil
		}))

	c.SubmitAgent("/researcher dig in", "researcher", "dig in")
	e, ok := recv(t, sub).(chat.TurnStartEvent)
	if !ok || e.Source != chat.SourceSpawn || e.Display != "/researcher dig in" || e.Input != "dig in" {
		t.Fatalf("want TurnStart(agent, display, task), got %#v", e)
	}
	mustTurnEnd(t, sub, "dig in") // echoModel echoes the child's task back as its answer
	mu.Lock()
	if gotName != "researcher" {
		t.Fatalf("charter resolver saw %q, want researcher", gotName)
	}
	mu.Unlock()
	if msgs := c.Snapshot().Messages; len(msgs) != 0 {
		t.Fatalf("spawn leaked into the parent conversation: %+v", msgs)
	}
}

// A SubmitAgent with no charter resolver wired, or an unknown agent, fails the turn
// (an error TurnEnd), never panics.
func TestChat_SubmitAgent_NoResolver_ErrorsTurn(t *testing.T) {
	c, sub := startChat(t, echoModel{})

	c.SubmitAgent("/x", "x", "go")
	if _, ok := recv(t, sub).(chat.TurnStartEvent); !ok {
		t.Fatal("want TurnStart")
	}
	if e, ok := recv(t, sub).(chat.TurnEndEvent); !ok || e.Err == nil {
		t.Fatalf("want TurnEnd with an error, got %#v", e)
	}
}

// Every turn carries the approval sink — the approval is always recorded on the chat
// (so a reconnecting client sees it), watched or not. Attendance is not latched at
// turn start; the router reads HasClients at Ask-time instead. So the sink is present
// regardless of subscribers, and HasClients reflects only REAL subscribers (a passive
// Tap must not count).
func TestChat_ApprovalSink_AlwaysStampedAndSeesRealClients(t *testing.T) {
	check := func(subscribe, tap bool) (hasSink, hasClients bool) {
		sinkCh, clientsCh := make(chan bool, 1), make(chan bool, 1)
		model := modelFunc(func(ctx context.Context, _ []brain.Message, _ []tool.Spec) (brain.Step, error) {
			s := chat.ApprovalSinkFrom(ctx)
			sinkCh <- s != nil
			clientsCh <- s != nil && s.HasClients()
			return brain.Step{Answer: "ok"}, nil
		})
		c := newChat(model)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		if subscribe {
			_, unsub := c.Subscribe()
			t.Cleanup(unsub)
		}
		if tap {
			_, unsub := c.Tap()
			t.Cleanup(unsub)
		}
		c.Start(ctx)
		c.Submit(chat.SourceUser, "go")
		select {
		case <-time.After(2 * time.Second):
			t.Fatal("turn did not run")
		case hasSink = <-sinkCh:
		}
		hasClients = <-clientsCh
		return hasSink, hasClients
	}

	if s, _ := check(false, false); !s {
		t.Error("unwatched turn missing its approval sink; it must always be recorded")
	}
	if s, c := check(true, false); !s || !c {
		t.Errorf("watched turn: sink=%v clients=%v; want both true", s, c)
	}
	if s, c := check(false, true); !s || c {
		t.Errorf("tapped-only turn: sink=%v clients=%v; want sink true, clients false (a tap is not a client)", s, c)
	}
}

// ClearPending drops a parked approval and announces it resolved — the path an out-of-band
// answer takes (the chat's own Resolve never ran). A second call emits nothing (a client
// that answered in-band already cleared it).
func TestChat_ClearPending_EmitsResolvedAndIdempotent(t *testing.T) {
	c := newChat(echoModel{}) // loop deliberately not started: Present/Clear only emit
	sub, unsub := c.Subscribe()
	t.Cleanup(unsub)

	c.PresentApproval("Send email", []string{"Allow", "Deny"}, func(int) {})
	appr, ok := recv(t, sub).(chat.ApprovalEvent)
	if !ok {
		t.Fatalf("want ApprovalEvent, got %#v", appr)
	}

	c.ClearPending()
	res, ok := recv(t, sub).(chat.ApprovalResolvedEvent)
	if !ok || res.ID != appr.ID {
		t.Fatalf("want ApprovalResolvedEvent id=%q, got %#v", appr.ID, res)
	}

	c.ClearPending() // idempotent: nothing left to clear
	select {
	case ev := <-sub:
		t.Fatalf("second ClearPending emitted %#v, want nothing", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func mustTurnStart(t *testing.T, sub <-chan chat.Event, input string, src chat.Source) {
	t.Helper()
	e, ok := recv(t, sub).(chat.TurnStartEvent)
	if !ok || e.Input != input || e.Source != src {
		t.Fatalf("want TurnStart(%s,%s), got %#v", input, src, e)
	}
}

func mustTurnEnd(t *testing.T, sub <-chan chat.Event, answer string) {
	t.Helper()
	e, ok := recv(t, sub).(chat.TurnEndEvent)
	if !ok || e.Answer != answer {
		t.Fatalf("want TurnEnd(%s), got %#v", answer, e)
	}
}
