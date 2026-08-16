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
	return runAskCapped(b, ctx, a, gate.RecallAlways, suggest)
}

// runAskCapped is runAsk with the policy ceiling spelled out, for the tests that are about it.
func runAskCapped(b *hitl.Broker, ctx context.Context, a gate.Action, ceiling gate.Recall, suggest []gate.Grant) <-chan askResult {
	ch := make(chan askResult, 1)
	go func() {
		approved, grant, recall, err := b.Ask(ctx, a, ceiling, suggest)
		ch <- askResult{approved, grant, recall, err}
	}()
	return ch
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
	approvals   []hitl.Approval
	resolutions []sinkResolved

	gotApproval chan hitl.Approval
	gotResolved chan sinkResolved
}

func newFakeSink() *fakeSink {
	return &fakeSink{
		gotApproval: make(chan hitl.Approval, 16),
		gotResolved: make(chan sinkResolved, 16),
	}
}

func (s *fakeSink) Approval(_ context.Context, a hitl.Approval) {
	call := a
	call.Options = slices.Clone(a.Options) // the broker shares the slice with every sink
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

	b.Resolve(call.ID, "once") // approve once (RecallNever)
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

// TestAsk_HumanDeny_NilError: an option id the approval never offered denies with a NIL error, so the
// gate surfaces the refusal to the model (distinct from a timeout). The empty string is the case a
// truncated or malformed message produces and is the reason the answer is an id and not an index:
// there is no zero value that approves.
func TestAsk_HumanDeny_NilError(t *testing.T) {
	tests := []struct {
		name   string
		option string
	}{
		{name: "explicit deny", option: hitl.DenyOption},
		{name: "omitted option is not an approval", option: ""},
		{name: "unknown option", option: "bogus"},
		{name: "widening that was never offered", option: "widen0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := hitl.NewBroker(nil, discard())
			sink := newFakeSink()
			b.Attach(context.Background(), sink)

			res := runAsk(b, context.Background(), gate.Action{Kind: "net", Target: "api.example.com"}, nil)
			call := <-sink.gotApproval
			b.Resolve(call.ID, tt.option)
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

// TestAsk_OptionMapping: choosing an option id yields exactly the grant and recall that option was
// presented with — "once" remembers nothing, "session" and "always" hold the EXACT target, and a
// "widen" option hands back the broader grant the tool suggested.
func TestAsk_OptionMapping(t *testing.T) {
	action := gate.Action{Kind: "net", Target: "api.github.com"}
	suggestion := gate.Grant{Kind: "net", Target: "*.github.com"}
	exact := gate.Grant{Kind: action.Kind, Target: action.Target}

	tests := []struct {
		name       string
		option     string
		suggest    []gate.Grant
		wantGrant  gate.Grant
		wantRecall gate.Recall
	}{
		{name: "approve once maps to no-recall grant", option: "once", wantGrant: exact, wantRecall: gate.RecallNever},
		{name: "approve session", option: "session", wantGrant: exact, wantRecall: gate.RecallSession},
		{name: "approve always", option: "always", wantGrant: exact, wantRecall: gate.RecallAlways},
		{name: "approve widening returns the broader grant", option: "widen0", suggest: []gate.Grant{suggestion}, wantGrant: suggestion, wantRecall: gate.RecallAlways},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := hitl.NewBroker(nil, discard())
			sink := newFakeSink()
			b.Attach(context.Background(), sink)

			res := runAsk(b, context.Background(), action, tt.suggest)
			call := <-sink.gotApproval
			b.Resolve(call.ID, tt.option)
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

// TestAsk_PresentsStructuredOptions: what a sink is handed is the action verbatim plus one option per
// answer, each carrying its own grant and recall — and only a suggested widening is marked as one, so
// no downstream layer has to compare targets to tell a widening from the exact grant.
func TestAsk_PresentsStructuredOptions(t *testing.T) {
	action := gate.Action{Kind: "net", Target: "api.github.com"}
	suggestion := gate.Grant{Kind: "net", Target: "*.github.com"}
	exact := gate.Grant{Kind: action.Kind, Target: action.Target}

	b := hitl.NewBroker(nil, discard())
	sink := newFakeSink()
	b.Attach(context.Background(), sink)

	res := runAsk(b, context.Background(), action, []gate.Grant{suggestion})
	call := <-sink.gotApproval

	if call.Action != action {
		t.Errorf("presented action = %+v, want %+v", call.Action, action)
	}
	want := []hitl.Option{
		{ID: "once", Recall: gate.RecallNever, Grant: exact},
		{ID: "session", Recall: gate.RecallSession, Grant: exact},
		{ID: "always", Recall: gate.RecallAlways, Grant: exact},
		{ID: "widen0", Recall: gate.RecallAlways, Grant: suggestion, Widens: true},
	}
	if !slices.Equal(call.Options, want) {
		t.Errorf("presented options = %+v, want %+v", call.Options, want)
	}

	b.Resolve(call.ID, "once")
	<-res
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

			b.Resolve(call.ID, "once")    // first answer: allow once (RecallNever) — lands in the buffered channel
			b.Resolve(call.ID, "session") // later answer: channel full, dropped by select-default

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
				b.Resolve(call.ID, []string{"once", "session"}[i%2]) // both options approve
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
		b.Resolve("deadbeef0000", "once") // must not panic and must have no effect
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
			b.Resolve(call.ID, "once")
			if got.approved {
				t.Errorf("approved = true, want false — a late resolve must not flip a concluded deny")
			}
		})
	})
}

// --- Edge ---------------------------------------------------------------------------------------

// TestAttach_RepresentsOpenApprovals_WithFrame: a device attaching mid-flight gets every open approval
// re-presented carrying the same id, frame, action, and — so both devices answer the identical
// question — the identical option set.
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

	if repr.ID != orig.ID {
		t.Errorf("re-presented id = %q, want %q", repr.ID, orig.ID)
	}
	if repr.Frame != orig.Frame {
		t.Errorf("re-presented frame = %d, want %d", repr.Frame, orig.Frame)
	}
	if repr.Action != orig.Action {
		t.Errorf("re-presented action = %+v, want %+v", repr.Action, orig.Action)
	}
	if !slices.Equal(repr.Options, orig.Options) {
		t.Errorf("re-presented options = %+v, want %+v", repr.Options, orig.Options)
	}

	b.Resolve(orig.ID, "once")
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

			b.Resolve(call.ID, "once")
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

		b.Resolve(call.ID, "once")
		<-res

		gotActive := <-active.gotResolved
		gotBackground := <-background.gotResolved // background sink is still told to clear
		if gotActive.id != call.ID {
			t.Errorf("active Resolved id = %q, want %q", gotActive.id, call.ID)
		}
		if gotBackground.id != call.ID {
			t.Errorf("background Resolved id = %q, want %q", gotBackground.id, call.ID)
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
		if gotResolved.id != call.ID {
			t.Errorf("Resolved id = %q, want %q", gotResolved.id, call.ID)
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

	if want := agentkit.FrameFrom(ctx); call.Frame != want {
		t.Errorf("presented frame = %d, want %d (= FrameFrom(ctx))", call.Frame, want)
	}

	b.Resolve(call.ID, "once")
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

	if call.ChatID != "chat42" {
		t.Errorf("presented chatID = %q, want %q", call.ChatID, "chat42")
	}

	b.Resolve(call.ID, "once")
	<-res
}

// TestAsk_PresentsActionVerbatim: a sink is handed the action as it was asked, targeted or not. The
// broker composes no sentence for a device — kind and target stay separate fields it renders itself.
func TestAsk_PresentsActionVerbatim(t *testing.T) {
	tests := []struct {
		name   string
		action gate.Action
	}{
		{name: "targeted", action: gate.Action{Kind: "net", Target: "api.example.com"}},
		{name: "targetless", action: gate.Action{Kind: "time.now"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := hitl.NewBroker(nil, discard())
			sink := newFakeSink()
			b.Attach(context.Background(), sink)

			res := runAsk(b, context.Background(), tt.action, nil)
			call := <-sink.gotApproval
			if call.Action != tt.action {
				t.Errorf("presented action = %+v, want %+v", call.Action, tt.action)
			}
			b.Resolve(call.ID, "once")
			<-res
		})
	}
}

// TestPush_TargetlessSummary: the ONE line the broker still renders is the push body, because iOS
// renders that string and cannot be handed structure. A targeted action reads "kind → target"
// (covered by TestAsk_NoActiveDevice_PushesThenAwaits); a targetless one is just the kind.
func TestPush_TargetlessSummary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pusher := newFakePusher()
		b := hitl.NewBroker(pusher, discard())

		res := runAsk(b, context.Background(), gate.Action{Kind: "time.now"}, nil)

		if got, want := <-pusher.pushed, "time.now"; got != want {
			t.Errorf("push body = %q, want %q", got, want)
		}
		<-res // never answered → denies on the timeout, draining the bubble
	})
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
	b.Resolve(call1.ID, "once")
	<-res1

	res2 := runAsk(b, context.Background(), action, nil)
	call2 := <-sink.gotApproval
	b.Resolve(call2.ID, "once")
	<-res2

	if !isHex12(call1.ID) {
		t.Errorf("id %q is not 12 lowercase hex chars", call1.ID)
	}
	if !isHex12(call2.ID) {
		t.Errorf("id %q is not 12 lowercase hex chars", call2.ID)
	}
	if call1.ID == call2.ID {
		t.Errorf("ids collide: both %q", call1.ID)
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

// A device whose class may not approve is handed NO broker at all, so its connection holds a nil
// *Broker for its whole life and calls the bookkeeping methods on it. They must do nothing rather
// than explode: this used to be a guard at every call site, and the one site that forgot —
// presence.set — turned a single message from the least-trusted class into a panic that killed the
// connection.
//
// Ask is deliberately absent here. What an absent approver ANSWERS is a permission decision, and
// gate owns it; a nil-tolerant answer in the broker would be a second place deciding the same thing.
func TestNilBroker_BookkeepingIsANoOp(t *testing.T) {
	var b *hitl.Broker // exactly what serve hands a connection that may not approve

	b.Attach(t.Context(), nopSink{})
	b.SetActive(t.Context(), nopSink{}, true)
	b.SetActive(t.Context(), nopSink{}, false)
	b.Resolve("some-id", "allow")
	b.Detach(nopSink{})

	if b.AnyActive() {
		t.Error("a nil broker reported somebody in the foreground")
	}
}

// nopSink is a Sink that records nothing — a nil broker must never reach it.
type nopSink struct{}

var _ hitl.Sink = nopSink{}

func (nopSink) Approval(context.Context, hitl.Approval) {}
func (nopSink) Resolved(context.Context, string)        {}

// TestOptionsUnderARecallNeverCeiling pins the sheet a phone renders for a kind that is asked every
// time: one answer, "once". Before the ceiling reached the approver it showed three buttons and a
// widening, and the gate quietly turned every one of them into "once" — so a person's "always" was
// taken, discarded, and asked for again the next day.
func TestOptionsUnderARecallNeverCeiling(t *testing.T) {
	action := gate.Action{Kind: "net", Target: "api.github.com"}
	suggest := []gate.Grant{{Kind: "net", Target: "*.github.com"}}

	b := hitl.NewBroker(nil, discard())
	sink := newFakeSink()
	b.Attach(context.Background(), sink)

	res := runAskCapped(b, context.Background(), action, gate.RecallNever, suggest)
	call := <-sink.gotApproval
	defer func() { b.Resolve(call.ID, hitl.DenyOption); <-res }()

	if len(call.Options) != 1 {
		t.Fatalf("offered %d answers under a never ceiling: %+v", len(call.Options), call.Options)
	}
	if o := call.Options[0]; o.ID != "once" || o.Recall != gate.RecallNever {
		t.Errorf("the single answer was %+v, want once/never", o)
	}
}

// TestOptionsUnderARecallSessionCeiling pins the middle case: "always" disappears, and the widening
// stays but as a session answer — its target is still worth broadening even when the answer cannot be
// kept forever.
func TestOptionsUnderARecallSessionCeiling(t *testing.T) {
	action := gate.Action{Kind: "net", Target: "api.github.com"}
	suggest := []gate.Grant{{Kind: "net", Target: "*.github.com"}}

	b := hitl.NewBroker(nil, discard())
	sink := newFakeSink()
	b.Attach(context.Background(), sink)

	res := runAskCapped(b, context.Background(), action, gate.RecallSession, suggest)
	call := <-sink.gotApproval
	defer func() { b.Resolve(call.ID, hitl.DenyOption); <-res }()

	var widen *hitl.Option
	for i, o := range call.Options {
		if o.Recall > gate.RecallSession {
			t.Errorf("answer %q offered recall %v above the ceiling", o.ID, o.Recall)
		}
		if o.ID == "always" {
			t.Error(`"always" was offered under a session ceiling`)
		}
		if o.Widens {
			widen = &call.Options[i]
		}
	}
	if widen == nil {
		t.Fatal("the widening was dropped under a session ceiling, where it is still meaningful")
	}
	if widen.Recall != gate.RecallSession {
		t.Errorf("the widening carries recall %v, want session", widen.Recall)
	}
}
