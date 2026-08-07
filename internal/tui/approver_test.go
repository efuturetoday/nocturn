package tui_test

import (
	"context"
	"errors"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/tui"
)

// answer is one Ask call running in its own goroutine, the way a turn calls it.
type answer struct {
	approved bool
	grant    gate.Grant
	recall   gate.Recall
	err      error
}

// ask starts an Ask and returns the pending ask the UI would present plus the channel its result
// arrives on. It blocks until the approver has presented, so no test needs a sleep.
func ask(t *testing.T, ctx context.Context, p *tui.Approver, a gate.Action, suggest ...gate.Grant) (*tui.Ask, <-chan answer) {
	t.Helper()
	out := make(chan answer, 1)
	go func() {
		approved, grant, recall, err := p.Ask(ctx, a, suggest)
		out <- answer{approved, grant, recall, err}
	}()
	return <-p.Asks(), out
}

func TestOptionsMirrorTheGateSemantics(t *testing.T) {
	p := tui.NewApprover()
	action := gate.Action{Kind: "net", Target: "api.example.com"}
	suggest := []gate.Grant{{Kind: "net", Target: "*.example.com"}}

	pending, _ := ask(t, t.Context(), p, action, suggest...)

	want := []struct {
		recall gate.Recall
		target string
		widens bool
	}{
		{gate.RecallNever, "api.example.com", false},
		{gate.RecallSession, "api.example.com", false},
		{gate.RecallAlways, "api.example.com", false},
		{gate.RecallAlways, "*.example.com", true},
	}
	if len(pending.Options) != len(want) {
		t.Fatalf("Options = %d, want %d", len(pending.Options), len(want))
	}
	for i, w := range want {
		got := pending.Options[i]
		if got.Recall != w.recall || got.Grant.Target != w.target || got.Widens != w.widens {
			t.Errorf("Options[%d] = %+v, want recall %v on %q (widens %v)", i, got, w.recall, w.target, w.widens)
		}
		if got.Label == "" {
			t.Errorf("Options[%d] has no label", i)
		}
	}
}

func TestResolveReturnsTheChosenOption(t *testing.T) {
	tests := map[string]struct {
		choose     int
		wantRecall gate.Recall
		wantTarget string
	}{
		"once":     {0, gate.RecallNever, "api.example.com"},
		"session":  {1, gate.RecallSession, "api.example.com"},
		"always":   {2, gate.RecallAlways, "api.example.com"},
		"widening": {3, gate.RecallAlways, "*.example.com"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			p := tui.NewApprover()
			pending, res := ask(t, t.Context(), p,
				gate.Action{Kind: "net", Target: "api.example.com"},
				gate.Grant{Kind: "net", Target: "*.example.com"})

			pending.Resolve(tt.choose)

			got := <-res
			if !got.approved || got.err != nil {
				t.Fatalf("Ask() = approved %v, err %v, want an approval", got.approved, got.err)
			}
			if got.recall != tt.wantRecall || got.grant.Target != tt.wantTarget {
				t.Errorf("Ask() = %v on %q, want %v on %q", got.recall, got.grant.Target, tt.wantRecall, tt.wantTarget)
			}
		})
	}
}

func TestAskBlocksUntilAnswered(t *testing.T) {
	p := tui.NewApprover()
	pending, res := ask(t, t.Context(), p, gate.Action{Kind: "file", Target: "/etc/passwd"})

	select {
	case got := <-res:
		t.Fatalf("Ask() returned %+v before anybody answered", got)
	default:
	}

	pending.Deny()

	if got := <-res; got.approved || got.err != nil {
		t.Errorf("Ask() = approved %v, err %v, want a refusal with no error", got.approved, got.err)
	}
}

func TestAnswersTheAskNeverOfferedAreRefusals(t *testing.T) {
	for name, choice := range map[string]int{"negative": -1, "past the end": 99} {
		t.Run(name, func(t *testing.T) {
			p := tui.NewApprover()
			pending, res := ask(t, t.Context(), p, gate.Action{Kind: "net", Target: "x"})

			pending.Resolve(choice)

			got := <-res
			if got.approved {
				t.Error("approved = true, want a refusal — no index may approve by default")
			}
			if got.err != nil {
				t.Errorf("err = %v, want nil — a deliberate no is not a failure", got.err)
			}
			if got.recall != gate.RecallNever {
				t.Errorf("recall = %v, want RecallNever", got.recall)
			}
		})
	}
}

func TestFirstAnswerWins(t *testing.T) {
	p := tui.NewApprover()
	pending, res := ask(t, t.Context(), p, gate.Action{Kind: "net", Target: "x"})

	pending.Resolve(0)
	pending.Resolve(2) // a double keypress must not upgrade the grant

	if got := <-res; got.recall != gate.RecallNever {
		t.Errorf("recall = %v, want RecallNever from the first answer", got.recall)
	}
}

func TestCancelledTurnUnblocksTheAsk(t *testing.T) {
	p := tui.NewApprover()
	ctx, cancel := context.WithCancel(t.Context())
	pending, res := ask(t, ctx, p, gate.Action{Kind: "net", Target: "x"})

	cancel()

	got := <-res
	if got.approved {
		t.Error("approved = true, want a refusal on cancellation")
	}
	if !errors.Is(got.err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", got.err)
	}
	if cleared := <-p.Cleared(); cleared != pending {
		t.Error("Cleared() did not report the abandoned ask — the modal would stay open")
	}
}

func TestCloseUnblocksTheAsk(t *testing.T) {
	p := tui.NewApprover()
	_, res := ask(t, t.Context(), p, gate.Action{Kind: "net", Target: "x"})

	p.Close()
	p.Close() // idempotent

	got := <-res
	if got.approved || !errors.Is(got.err, tui.ErrClosed) {
		t.Errorf("Ask() = approved %v, err %v, want ErrClosed", got.approved, got.err)
	}
}

func TestAskAfterCloseDoesNotBlock(t *testing.T) {
	p := tui.NewApprover()
	p.Close()

	approved, _, recall, err := p.Ask(t.Context(), gate.Action{Kind: "net", Target: "x"}, nil)

	if approved || recall != gate.RecallNever || !errors.Is(err, tui.ErrClosed) {
		t.Errorf("Ask() = %v/%v/%v, want a closed refusal", approved, recall, err)
	}
}

func TestAnsweringAnAbandonedAskIsHarmless(t *testing.T) {
	p := tui.NewApprover()
	ctx, cancel := context.WithCancel(t.Context())
	pending, res := ask(t, ctx, p, gate.Action{Kind: "net", Target: "x"})
	cancel()
	<-res

	pending.Resolve(2) // the modal was still on screen when the turn died
}
