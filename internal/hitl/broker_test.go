package hitl_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/tools"
)

// discard returns a logger that drops everything, keeping test output quiet.
func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// askResult bundles the four return values of Broker.Ask so a test can read them off a channel.
type askResult struct {
	approved bool
	grant    gate.Grant
	recall   gate.Recall
	err      error
}

// runAsk drives Broker.Ask on its own goroutine and hands the result back on a buffered channel, so
// the Ask goroutine never blocks after answering. Call it from inside a synctest bubble for the
// timeout cases; the started goroutine joins the caller's bubble.
func runAsk(b *hitl.Broker, ctx context.Context, a gate.Action, suggest []gate.Grant) <-chan askResult {
	ch := make(chan askResult, 1)
	go func() {
		approved, grant, recall, err := b.Ask(ctx, a, suggest)
		ch <- askResult{approved, grant, recall, err}
	}()
	return ch
}

// sinkApproval is one recorded Approval presentation.
type sinkApproval struct {
	id      string
	frame   uint64
	chatID  string
	intent  string
	options []string
}

// sinkResolved is one recorded Resolved call, keeping the ctx error so a test can prove conclude ran
// on a non-cancelled context (WithoutCancel).
type sinkResolved struct {
	id     string
	ctxErr error
}

// fakeSink is a race-safe hitl.Sink that records presentations and resolutions and signals each on a
// buffered channel so a test can await them without sleeping.
type fakeSink struct {
	mu          sync.Mutex
	approvals   []sinkApproval
	resolutions []sinkResolved

	gotApproval chan sinkApproval
	gotResolved chan sinkResolved
}

func newFakeSink() *fakeSink {
	return &fakeSink{
		gotApproval: make(chan sinkApproval, 16),
		gotResolved: make(chan sinkResolved, 16),
	}
}

func (s *fakeSink) Approval(_ context.Context, id string, frame uint64, chatID, intent string, options []string) {
	call := sinkApproval{id: id, frame: frame, chatID: chatID, intent: intent, options: slices.Clone(options)}
	s.mu.Lock()
	s.approvals = append(s.approvals, call)
	s.mu.Unlock()
	select {
	case s.gotApproval <- call:
	default:
	}
}

func (s *fakeSink) Resolved(ctx context.Context, id string) {
	call := sinkResolved{id: id, ctxErr: ctx.Err()}
	s.mu.Lock()
	s.resolutions = append(s.resolutions, call)
	s.mu.Unlock()
	select {
	case s.gotResolved <- call:
	default:
	}
}

func (s *fakeSink) approvalCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.approvals)
}

// fakePusher is a race-safe hitl.Pusher that records intents and signals each push.
type fakePusher struct {
	mu      sync.Mutex
	intents []string
	err     error

	pushed chan string
}

func newFakePusher() *fakePusher { return &fakePusher{pushed: make(chan string, 16)} }

func (p *fakePusher) Push(_ context.Context, intent string) error {
	p.mu.Lock()
	p.intents = append(p.intents, intent)
	err := p.err
	p.mu.Unlock()
	select {
	case p.pushed <- intent:
	default:
	}
	return err
}

func (p *fakePusher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.intents)
}

// --- Critical (fail-closed) ---------------------------------------------------------------------

// TestAsk_Timeout_FailsClosed: an approval presented to an attached device but never answered denies
// after approvalTimeout with ErrApprovalTimeout. synctest advances the fake clock to the 2m timer.
func TestAsk_Timeout_FailsClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := hitl.NewBroker(nil, discard())
		sink := newFakeSink()
		b.Attach(context.Background(), sink)

		res := <-runAsk(b, context.Background(), gate.Action{Kind: "net", Target: "api.example.com"}, nil)

		if res.approved {
			t.Errorf("approved = true, want false (fail-closed)")
		}
		if res.grant != (gate.Grant{}) {
			t.Errorf("grant = %+v, want zero", res.grant)
		}
		if res.recall != gate.RecallNever {
			t.Errorf("recall = %v, want RecallNever", res.recall)
		}
		if !errors.Is(res.err, hitl.ErrApprovalTimeout) {
			t.Errorf("err = %v, want ErrApprovalTimeout", res.err)
		}
		if sink.approvalCount() != 1 {
			t.Errorf("sink presentations = %d, want 1", sink.approvalCount())
		}
	})
}

// TestAsk_NoActiveDevice_NoPusher_DeniesOnTimeout: no device attached and no Pusher — the request is
// logged and denies on the timeout.
func TestAsk_NoActiveDevice_NoPusher_DeniesOnTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := hitl.NewBroker(nil, discard())

		res := <-runAsk(b, context.Background(), gate.Action{Kind: "net", Target: "api.example.com"}, nil)

		if res.approved {
			t.Errorf("approved = true, want false")
		}
		if res.recall != gate.RecallNever {
			t.Errorf("recall = %v, want RecallNever", res.recall)
		}
		if !errors.Is(res.err, hitl.ErrApprovalTimeout) {
			t.Errorf("err = %v, want ErrApprovalTimeout", res.err)
		}
	})
}

// TestAsk_NoActiveDevice_PushesThenAwaits: with no device attached the Pusher is woken exactly once
// with the intent; a device that then attaches gets the open approval re-presented and resolves it.
func TestAsk_NoActiveDevice_PushesThenAwaits(t *testing.T) {
	pusher := newFakePusher()
	b := hitl.NewBroker(pusher, discard())
	action := gate.Action{Kind: "net", Target: "api.example.com"}

	res := runAsk(b, context.Background(), action, nil)

	gotIntent := <-pusher.pushed // Push fired
	if want := "net → api.example.com"; gotIntent != want {
		t.Errorf("push intent = %q, want %q", gotIntent, want)
	}

	// A device attaches after the push and gets the open approval re-presented.
	sink := newFakeSink()
	b.Attach(context.Background(), sink)
	call := <-sink.gotApproval

	b.Resolve(call.id, 0) // approve once (RecallNever)
	got := <-res

	if pusher.count() != 1 {
		t.Errorf("push count = %d, want 1", pusher.count())
	}
	if !got.approved {
		t.Errorf("approved = false, want true after resolve")
	}
	if got.recall != gate.RecallNever {
		t.Errorf("recall = %v, want RecallNever", got.recall)
	}
}

// TestAsk_HumanDeny_NilError: a deliberate human "no" (index -1) or an out-of-range index denies with
// a NIL error, so the gate surfaces the refusal to the model (distinct from a timeout).
func TestAsk_HumanDeny_NilError(t *testing.T) {
	tests := []struct {
		name   string
		choice int
	}{
		{name: "explicit deny (-1)", choice: -1},
		{name: "out of range (99)", choice: 99},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := hitl.NewBroker(nil, discard())
			sink := newFakeSink()
			b.Attach(context.Background(), sink)

			res := runAsk(b, context.Background(), gate.Action{Kind: "net", Target: "api.example.com"}, nil)
			call := <-sink.gotApproval
			b.Resolve(call.id, tt.choice)
			got := <-res

			if got.approved {
				t.Errorf("approved = true, want false")
			}
			if got.grant != (gate.Grant{}) {
				t.Errorf("grant = %+v, want zero", got.grant)
			}
			if got.recall != gate.RecallNever {
				t.Errorf("recall = %v, want RecallNever", got.recall)
			}
			if got.err != nil {
				t.Errorf("err = %v, want nil (deliberate human no)", got.err)
			}
		})
	}
}

// TestAsk_ApproveIndexMapping: index 0 = once (RecallNever), 1 = session, 2 = always, 3.. = a suggested
// widening (always).
func TestAsk_ApproveIndexMapping(t *testing.T) {
	action := gate.Action{Kind: "net", Target: "api.github.com"}
	suggestion := gate.Grant{Kind: "net", Target: "*.github.com"}
	exact := gate.Grant{Kind: action.Kind, Target: action.Target}

	tests := []struct {
		name       string
		choice     int
		suggest    []gate.Grant
		wantGrant  gate.Grant
		wantRecall gate.Recall
	}{
		{name: "approve once maps to no-recall grant", choice: 0, wantGrant: exact, wantRecall: gate.RecallNever},
		{name: "approve session", choice: 1, wantGrant: exact, wantRecall: gate.RecallSession},
		{name: "approve always", choice: 2, wantGrant: exact, wantRecall: gate.RecallAlways},
		{name: "approve suggestion returns widened grant", choice: 3, suggest: []gate.Grant{suggestion}, wantGrant: suggestion, wantRecall: gate.RecallAlways},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := hitl.NewBroker(nil, discard())
			sink := newFakeSink()
			b.Attach(context.Background(), sink)

			res := runAsk(b, context.Background(), action, tt.suggest)
			call := <-sink.gotApproval
			b.Resolve(call.id, tt.choice)
			got := <-res

			if !got.approved {
				t.Fatalf("approved = false, want true")
			}
			if got.grant != tt.wantGrant {
				t.Errorf("grant = %+v, want %+v", got.grant, tt.wantGrant)
			}
			if got.recall != tt.wantRecall {
				t.Errorf("recall = %v, want %v", got.recall, tt.wantRecall)
			}
			if got.err != nil {
				t.Errorf("err = %v, want nil", got.err)
			}
		})
	}
}

// TestAsk_CtxCanceled_ReturnsCtxErr: a torn-down turn returns ctx.Err(), distinct from a timeout or a
// human deny, and still fails closed.
func TestAsk_CtxCanceled_ReturnsCtxErr(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b := hitl.NewBroker(nil, discard())
	sink := newFakeSink()
	b.Attach(context.Background(), sink)

	res := runAsk(b, ctx, gate.Action{Kind: "net", Target: "api.example.com"}, nil)
	<-sink.gotApproval // Ask has presented and is now awaiting a decision
	cancel()
	got := <-res

	if got.approved {
		t.Errorf("approved = true, want false")
	}
	if got.recall != gate.RecallNever {
		t.Errorf("recall = %v, want RecallNever", got.recall)
	}
	if !errors.Is(got.err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", got.err)
	}
	if errors.Is(got.err, hitl.ErrApprovalTimeout) {
		t.Errorf("err is ErrApprovalTimeout, want context.Canceled")
	}
}

// TestAsk_FirstAnswerWins: the first committed decision wins; a later Resolve for the same id is
// dropped (buffered channel + select-default). The concurrent subtest stresses the drop under -race.
func TestAsk_FirstAnswerWins(t *testing.T) {
	action := gate.Action{Kind: "net", Target: "api.example.com"}

	t.Run("deterministic first wins", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			b := hitl.NewBroker(nil, discard())
			sink := newFakeSink()
			b.Attach(context.Background(), sink)

			res := runAsk(b, context.Background(), action, nil)
			synctest.Wait() // Ask has presented and is durably blocked in its select
			call := <-sink.gotApproval

			b.Resolve(call.id, 0) // first answer: allow once (RecallNever) — lands in the buffered channel
			b.Resolve(call.id, 1) // later answer: channel full, dropped by select-default

			got := <-res
			if !got.approved {
				t.Fatalf("approved = false, want true")
			}
			if got.recall != gate.RecallNever {
				t.Errorf("recall = %v, want RecallNever (first answer, index 0, won)", got.recall)
			}
		})
	})

	t.Run("concurrent no race", func(t *testing.T) {
		b := hitl.NewBroker(nil, discard())
		sink := newFakeSink()
		b.Attach(context.Background(), sink)

		res := runAsk(b, context.Background(), action, nil)
		call := <-sink.gotApproval

		var wg sync.WaitGroup
		for i := range 20 {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				b.Resolve(call.id, i%2) // indices 0 and 1 both approve
			}(i)
		}
		got := <-res
		wg.Wait()

		if !got.approved {
			t.Errorf("approved = false, want true (some resolve won)")
		}
		if got.err != nil {
			t.Errorf("err = %v, want nil", got.err)
		}
	})
}

// TestResolve_ForgesDenyCannotBecomeApprove: a Resolve for an unknown or already-concluded id is a
// no-op — a late/forged approve can never flip a decision that already fell closed.
func TestResolve_ForgesDenyCannotBecomeApprove(t *testing.T) {
	t.Run("unknown id is a no-op", func(t *testing.T) {
		b := hitl.NewBroker(nil, discard())
		b.Resolve("deadbeef0000", 0) // must not panic and must have no effect
	})

	t.Run("late resolve after timeout stays denied", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			b := hitl.NewBroker(nil, discard())
			sink := newFakeSink()
			b.Attach(context.Background(), sink)

			res := runAsk(b, context.Background(), gate.Action{Kind: "net", Target: "api.example.com"}, nil)
			call := <-sink.gotApproval

			got := <-res // never resolved → denies on the timeout
			if !errors.Is(got.err, hitl.ErrApprovalTimeout) {
				t.Fatalf("err = %v, want ErrApprovalTimeout", got.err)
			}

			// A forged approve arriving after the deny concluded is a no-op; the result already stood.
			b.Resolve(call.id, 0)
			if got.approved {
				t.Errorf("approved = true, want false — a late resolve must not flip a concluded deny")
			}
		})
	})
}

// --- Edge ---------------------------------------------------------------------------------------

// TestAttach_RepresentsOpenApprovals_WithFrame: a device attaching mid-flight gets every open approval
// re-presented carrying the same id, frame, intent, and options.
func TestAttach_RepresentsOpenApprovals_WithFrame(t *testing.T) {
	b := hitl.NewBroker(nil, discard())
	first := newFakeSink()
	b.Attach(context.Background(), first)

	res := runAsk(b, context.Background(), gate.Action{Kind: "net", Target: "api.example.com"}, nil)
	orig := <-first.gotApproval

	// A second device attaches while the approval is open.
	second := newFakeSink()
	b.Attach(context.Background(), second)
	repr := <-second.gotApproval

	if repr.id != orig.id {
		t.Errorf("re-presented id = %q, want %q", repr.id, orig.id)
	}
	if repr.frame != orig.frame {
		t.Errorf("re-presented frame = %d, want %d", repr.frame, orig.frame)
	}
	if repr.intent != orig.intent {
		t.Errorf("re-presented intent = %q, want %q", repr.intent, orig.intent)
	}
	if !slices.Equal(repr.options, orig.options) {
		t.Errorf("re-presented options = %v, want %v", repr.options, orig.options)
	}

	b.Resolve(orig.id, 0)
	<-res
}

// TestSetActive covers foreground re-presentation and background non-routing.
func TestSetActive(t *testing.T) {
	action := gate.Action{Kind: "net", Target: "api.example.com"}

	t.Run("foreground represents", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			b := hitl.NewBroker(nil, discard())
			sink := newFakeSink()
			b.Attach(context.Background(), sink)
			b.SetActive(context.Background(), sink, false) // background before the ask

			res := runAsk(b, context.Background(), action, nil)
			synctest.Wait() // Ask registered the pending approval and blocked

			if sink.approvalCount() != 0 {
				t.Fatalf("background sink presentations = %d, want 0", sink.approvalCount())
			}

			b.SetActive(context.Background(), sink, true) // comes to the foreground → re-present
			call := <-sink.gotApproval
			if sink.approvalCount() != 1 {
				t.Errorf("presentations after foreground = %d, want 1", sink.approvalCount())
			}

			b.Resolve(call.id, 0)
			<-res
		})
	})

	t.Run("background not routed", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			b := hitl.NewBroker(nil, discard())
			sink := newFakeSink()
			b.Attach(context.Background(), sink)
			b.SetActive(context.Background(), sink, false)

			res := runAsk(b, ctx, action, nil)
			synctest.Wait() // Ask ran past routing and is blocked

			if sink.approvalCount() != 0 {
				t.Errorf("background sink presentations = %d, want 0", sink.approvalCount())
			}

			cancel() // release the blocked Ask so the bubble can drain
			<-res
		})
	})
}

// TestConclude covers clearing prompts on all sinks and running on a non-cancelled context.
func TestConclude(t *testing.T) {
	action := gate.Action{Kind: "net", Target: "api.example.com"}

	t.Run("clears prompt on all sinks", func(t *testing.T) {
		b := hitl.NewBroker(nil, discard())
		active := newFakeSink()
		b.Attach(context.Background(), active)
		background := newFakeSink()
		b.Attach(context.Background(), background)
		b.SetActive(context.Background(), background, false)

		res := runAsk(b, context.Background(), action, nil)
		call := <-active.gotApproval

		b.Resolve(call.id, 0)
		<-res

		gotActive := <-active.gotResolved
		gotBackground := <-background.gotResolved // background sink is still told to clear
		if gotActive.id != call.id {
			t.Errorf("active Resolved id = %q, want %q", gotActive.id, call.id)
		}
		if gotBackground.id != call.id {
			t.Errorf("background Resolved id = %q, want %q", gotBackground.id, call.id)
		}
		if background.approvalCount() != 0 {
			t.Errorf("background presentations = %d, want 0 (never routed, only cleared)", background.approvalCount())
		}
	})

	t.Run("runs on without cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		b := hitl.NewBroker(nil, discard())
		sink := newFakeSink()
		b.Attach(context.Background(), sink)

		res := runAsk(b, ctx, action, nil)
		call := <-sink.gotApproval

		cancel() // tear the turn down
		got := <-res
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", got.err)
		}

		// conclude still clears the prompt, and on a non-cancelled context (WithoutCancel).
		gotResolved := <-sink.gotResolved
		if gotResolved.id != call.id {
			t.Errorf("Resolved id = %q, want %q", gotResolved.id, call.id)
		}
		if gotResolved.ctxErr != nil {
			t.Errorf("Resolved ctx err = %v, want nil (conclude runs WithoutCancel)", gotResolved.ctxErr)
		}
	})
}

// TestAsk_FrameFromCtx_PropagatedToSink: Ask reads the tool-call frame from ctx and forwards it to the
// sink unchanged. agentkit exposes no frame setter to an external package, so this covers the
// top-level (frame 0) case and proves the value is taken from the ctx rather than hardcoded.
func TestAsk_FrameFromCtx_PropagatedToSink(t *testing.T) {
	ctx := context.Background()
	b := hitl.NewBroker(nil, discard())
	sink := newFakeSink()
	b.Attach(ctx, sink)

	res := runAsk(b, ctx, gate.Action{Kind: "net", Target: "api.example.com"}, nil)
	call := <-sink.gotApproval

	if want := agentkit.FrameFrom(ctx); call.frame != want {
		t.Errorf("presented frame = %d, want %d (= FrameFrom(ctx))", call.frame, want)
	}

	b.Resolve(call.id, 0)
	<-res
}

// TestAsk_ChatIDFromCtx_PropagatedToSink: Ask reads the raising chat id from ctx (stamped by the chat
// manager via tools.WithChatID) and forwards it to the sink for provenance.
func TestAsk_ChatIDFromCtx_PropagatedToSink(t *testing.T) {
	ctx := tools.WithChatID(context.Background(), "chat42")
	b := hitl.NewBroker(nil, discard())
	sink := newFakeSink()
	b.Attach(ctx, sink)

	res := runAsk(b, ctx, gate.Action{Kind: "net", Target: "api.example.com"}, nil)
	call := <-sink.gotApproval

	if call.chatID != "chat42" {
		t.Errorf("presented chatID = %q, want %q", call.chatID, "chat42")
	}

	b.Resolve(call.id, 0)
	<-res
}

// TestIntentOf_TargetlessVsTargeted checks the presented intent string through the public surface: a
// targeted action reads "Kind → Target", a targetless one is just the Kind.
func TestIntentOf_TargetlessVsTargeted(t *testing.T) {
	tests := []struct {
		name       string
		action     gate.Action
		wantIntent string
	}{
		{name: "targeted", action: gate.Action{Kind: "net", Target: "api.example.com"}, wantIntent: "net → api.example.com"},
		{name: "targetless", action: gate.Action{Kind: "time.now"}, wantIntent: "time.now"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := hitl.NewBroker(nil, discard())
			sink := newFakeSink()
			b.Attach(context.Background(), sink)

			res := runAsk(b, context.Background(), tt.action, nil)
			call := <-sink.gotApproval
			if call.intent != tt.wantIntent {
				t.Errorf("intent = %q, want %q", call.intent, tt.wantIntent)
			}
			b.Resolve(call.id, 0)
			<-res
		})
	}
}

// TestNewID_UniqueHex12 checks the prompt id observed at the sink: 12 lowercase hex chars, and distinct
// across two asks.
func TestNewID_UniqueHex12(t *testing.T) {
	b := hitl.NewBroker(nil, discard())
	sink := newFakeSink()
	b.Attach(context.Background(), sink)
	action := gate.Action{Kind: "net", Target: "api.example.com"}

	res1 := runAsk(b, context.Background(), action, nil)
	call1 := <-sink.gotApproval
	b.Resolve(call1.id, 0)
	<-res1

	res2 := runAsk(b, context.Background(), action, nil)
	call2 := <-sink.gotApproval
	b.Resolve(call2.id, 0)
	<-res2

	if !isHex12(call1.id) {
		t.Errorf("id %q is not 12 lowercase hex chars", call1.id)
	}
	if !isHex12(call2.id) {
		t.Errorf("id %q is not 12 lowercase hex chars", call2.id)
	}
	if call1.id == call2.id {
		t.Errorf("ids collide: both %q", call1.id)
	}
}

func isHex12(s string) bool {
	if len(s) != 12 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// TestDetach_RemovesSink: after Detach a connection is neither routed an approval nor told to clear.
func TestDetach_RemovesSink(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		b := hitl.NewBroker(nil, discard())
		sink := newFakeSink()
		b.Attach(context.Background(), sink)
		b.Detach(sink)

		res := runAsk(b, ctx, gate.Action{Kind: "net", Target: "api.example.com"}, nil)
		synctest.Wait() // Ask ran past routing and is blocked

		if sink.approvalCount() != 0 {
			t.Errorf("detached sink presentations = %d, want 0", sink.approvalCount())
		}

		cancel()
		<-res

		// conclude iterates only attached sinks; the detached one is never told to clear.
		select {
		case r := <-sink.gotResolved:
			t.Errorf("detached sink got Resolved(%q), want none", r.id)
		default:
		}
	})
}
