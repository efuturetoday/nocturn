package gate_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
)

// --- shared fakes (external package: exercise the public seam only) ---

// fakeApprover is a scripted gate.Approver. It records how often it was asked and with what, returns a
// canned (approved, grant, recall, err), and can optionally block until released — so a test can hold a
// human "deciding" while it inspects the paused turn clock.
type fakeApprover struct {
	// scripted response
	approved bool
	grant    gate.Grant
	recall   gate.Recall
	err      error

	// optional coordination (nil = return immediately)
	entered chan struct{} // signalled once Ask is entered (buffered by the test)
	block   chan struct{} // Ask waits on this (or ctx) before returning

	mu          sync.Mutex
	calls       int
	lastAction  gate.Action
	lastSuggest []gate.Grant
	lastCeiling gate.Recall
}

var _ gate.Approver = (*fakeApprover)(nil)

func (f *fakeApprover) Ask(ctx context.Context, a gate.Action, ceiling gate.Recall, suggest []gate.Grant) (bool, gate.Grant, gate.Recall, error) {
	f.mu.Lock()
	f.calls++
	f.lastAction = a
	f.lastSuggest = suggest
	f.lastCeiling = ceiling
	f.mu.Unlock()

	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return false, gate.Grant{}, gate.RecallNever, ctx.Err()
		}
	}
	return f.approved, f.grant, f.recall, f.err
}

func (f *fakeApprover) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// rememberCall is one Remember the gate made on the grant store.
type rememberCall struct {
	grant  gate.Grant
	recall gate.Recall
}

// spyGrants is a gate.Grants that records both queries and writes — so a test can assert whether the
// standing-grant cache was consulted at all, and at what Recall an approval was remembered.
type spyGrants struct {
	allow bool // what Allowed reports

	mu         sync.Mutex
	allowCalls int
	remembered []rememberCall
}

func (s *spyGrants) Allowed(a gate.Action, _ gate.Matcher) bool {
	s.mu.Lock()
	s.allowCalls++
	s.mu.Unlock()
	return s.allow
}

func (s *spyGrants) Remember(g gate.Grant, r gate.Recall) {
	s.mu.Lock()
	s.remembered = append(s.remembered, rememberCall{grant: g, recall: r})
	s.mu.Unlock()
}

// Forget completes the interface. Nothing in gate calls it — Check never revokes — so a spy that
// recorded the call would be recording something no test can trigger.
func (s *spyGrants) Forget(gate.Grant) bool { return false }

func (s *spyGrants) allowCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.allowCalls
}

func (s *spyGrants) writes() []rememberCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]rememberCall(nil), s.remembered...)
}

// policyReturning is a gate.Policy that hands back a fixed Ruling regardless of the action.
func policyReturning(r gate.Ruling) gate.Policy {
	return gate.PolicyFunc(func(gate.Action) gate.Ruling { return r })
}

var testAction = gate.Action{Kind: "net", Target: "example.com"}

// --- Check ---

// Without any machinery installed, gating is opt-in: Check is fully open and never denies.
func TestCheck_NoMachinery_IsOpen(t *testing.T) {
	if err := gate.Check(context.Background(), testAction, nil); err != nil {
		t.Fatalf("Check with no machinery = %v, want nil", err)
	}
}

// A policy Allow returns nil and never reaches the human — the approver must not be asked.
func TestCheck_PolicyAllow_ReturnsNil(t *testing.T) {
	appr := &fakeApprover{approved: true}
	ctx := gate.With(context.Background(), policyReturning(gate.Allowed()), gate.NewMemGrants(), appr)

	if err := gate.Check(ctx, testAction, nil); err != nil {
		t.Fatalf("Check on Allow = %v, want nil", err)
	}
	if n := appr.callCount(); n != 0 {
		t.Fatalf("approver asked %d times on Allow, want 0", n)
	}
}

// A policy Deny returns ErrDenied without consulting the human.
func TestCheck_PolicyDeny_ReturnsErrDenied(t *testing.T) {
	appr := &fakeApprover{approved: true}
	ctx := gate.With(context.Background(), policyReturning(gate.Denied()), gate.NewMemGrants(), appr)

	err := gate.Check(ctx, testAction, nil)
	if !errors.Is(err, gate.ErrDenied) {
		t.Fatalf("Check on Deny = %v, want ErrDenied", err)
	}
	if n := appr.callCount(); n != 0 {
		t.Fatalf("approver asked %d times on Deny, want 0", n)
	}
}

// Deny is not overridable: a covering standing grant does not rescue a denied action, and the grant
// cache is never even consulted (Deny short-circuits before it).
func TestCheck_DenyBeatsGrant(t *testing.T) {
	grants := &spyGrants{allow: true} // would cover the action if consulted
	appr := &fakeApprover{approved: true}
	ctx := gate.With(context.Background(), policyReturning(gate.Denied()), grants, appr)

	err := gate.Check(ctx, testAction, nil)
	if !errors.Is(err, gate.ErrDenied) {
		t.Fatalf("Check = %v, want ErrDenied despite covering grant", err)
	}
	if n := grants.allowCount(); n != 0 {
		t.Fatalf("grant cache consulted %d times on Deny, want 0", n)
	}
	if n := appr.callCount(); n != 0 {
		t.Fatalf("approver asked %d times on Deny, want 0", n)
	}
}

// An Ask whose Recall permits caching is satisfied by a covering standing grant — no human needed, even
// unattended (Approver nil).
func TestCheck_Ask_CoveringGrant_NoApprover(t *testing.T) {
	grants := gate.NewMemGrants(gate.Grant{Kind: "net", Target: "example.com"})
	ctx := gate.With(context.Background(), policyReturning(gate.AskWith(gate.RecallSession)), grants, nil)

	if err := gate.Check(ctx, testAction, nil); err != nil {
		t.Fatalf("Check with covering grant = %v, want nil", err)
	}
}

// An Ask with no covering grant and no Approver (unattended) fails closed.
func TestCheck_Ask_NoApprover_Unattended_Denied(t *testing.T) {
	ctx := gate.With(context.Background(), policyReturning(gate.AskWith(gate.RecallSession)), gate.NewMemGrants(), nil)

	err := gate.Check(ctx, testAction, nil)
	if !errors.Is(err, gate.ErrDenied) {
		t.Fatalf("Check unattended Ask = %v, want ErrDenied", err)
	}
}

// An approved Ask is remembered at the MORE RESTRICTIVE of the policy ceiling and the human's choice:
// policy RecallSession caps a human's RecallAlways down to RecallSession.
func TestCheck_Ask_ApproverApproves_Remembers(t *testing.T) {
	widened := gate.Grant{Kind: "net", Target: "*.example.com"}
	grants := &spyGrants{allow: false}
	appr := &fakeApprover{approved: true, grant: widened, recall: gate.RecallAlways}
	ctx := gate.With(context.Background(), policyReturning(gate.AskWith(gate.RecallSession)), grants, appr)

	if err := gate.Check(ctx, testAction, nil, widened); err != nil {
		t.Fatalf("Check on approval = %v, want nil", err)
	}
	writes := grants.writes()
	if len(writes) != 1 {
		t.Fatalf("Remember called %d times, want 1", len(writes))
	}
	if got := writes[0].recall; got != gate.RecallSession {
		t.Fatalf("remembered at recall %v, want RecallSession (policy ceiling)", got)
	}
	if got := writes[0].grant; got != widened {
		t.Fatalf("remembered grant %+v, want %+v (the approved grant)", got, widened)
	}
	if got := appr.lastSuggest; len(got) != 1 || got[0] != widened {
		t.Fatalf("approver saw suggestions %+v, want [%+v]", got, widened)
	}
}

// A declined Ask returns ErrDenied and remembers nothing.
func TestCheck_Ask_ApproverDenies_ReturnsErrDenied(t *testing.T) {
	grants := &spyGrants{allow: false}
	appr := &fakeApprover{approved: false}
	ctx := gate.With(context.Background(), policyReturning(gate.AskWith(gate.RecallSession)), grants, appr)

	err := gate.Check(ctx, testAction, nil)
	if !errors.Is(err, gate.ErrDenied) {
		t.Fatalf("Check on decline = %v, want ErrDenied", err)
	}
	if w := grants.writes(); len(w) != 0 {
		t.Fatalf("remembered %d grants on decline, want 0", len(w))
	}
}

// RecallNever means irreversible: the grant cache is skipped entirely (never consulted), so a human is
// asked every time even when a covering grant exists.
func TestCheck_RecallNever_SkipsGrantCache(t *testing.T) {
	grants := &spyGrants{allow: true} // covering — but must be bypassed
	appr := &fakeApprover{approved: true, recall: gate.RecallAlways}
	ctx := gate.With(context.Background(), policyReturning(gate.AskWith(gate.RecallNever)), grants, appr)

	if err := gate.Check(ctx, testAction, nil); err != nil {
		t.Fatalf("Check = %v, want nil (approved)", err)
	}
	if n := grants.allowCount(); n != 0 {
		t.Fatalf("grant cache consulted %d times under RecallNever, want 0", n)
	}
	if n := appr.callCount(); n != 1 {
		t.Fatalf("approver asked %d times under RecallNever, want 1", n)
	}
}

// Under RecallNever the effective recall is min(Never, chosen) = Never, so an approval is honored once
// but nothing is remembered.
func TestCheck_RecallNever_ApproverApproves_NotRemembered(t *testing.T) {
	grants := &spyGrants{allow: false}
	appr := &fakeApprover{approved: true, grant: gate.Grant{Kind: "net", Target: "example.com"}, recall: gate.RecallAlways}
	ctx := gate.With(context.Background(), policyReturning(gate.AskWith(gate.RecallNever)), grants, appr)

	if err := gate.Check(ctx, testAction, nil); err != nil {
		t.Fatalf("Check = %v, want nil", err)
	}
	if w := grants.writes(); len(w) != 0 {
		t.Fatalf("remembered %d grants under RecallNever, want 0", len(w))
	}
}

// An approver error is wrapped as "gate: approver: …" and is distinct from ErrDenied — the model sees a
// real failure, not a policy refusal.
func TestCheck_ApproverError_Wrapped(t *testing.T) {
	boom := errors.New("boom")
	appr := &fakeApprover{err: boom}
	ctx := gate.With(context.Background(), policyReturning(gate.AskWith(gate.RecallSession)), gate.NewMemGrants(), appr)

	err := gate.Check(ctx, testAction, nil)
	if err == nil {
		t.Fatal("Check on approver error = nil, want wrapped error")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("error %v does not wrap the approver error", err)
	}
	if errors.Is(err, gate.ErrDenied) {
		t.Fatalf("approver error %v must not be ErrDenied", err)
	}
	if want := "gate: approver: "; err.Error()[:len(want)] != want {
		t.Fatalf("error %q, want prefix %q", err.Error(), want)
	}
}

// The turn's pausable wall-clock is stopped around the out-of-band Ask: a human deciding for far longer
// than the budget must not trip the deadline, and the budget is restored intact on resume. Uses
// synctest so synthetic time can jump past the budget deterministically while the approver blocks.
func TestCheck_PausesTurnClockAroundAsk(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const budget = 5 * time.Second

		base, cancel := agentkit.WithPausableBudget(context.Background(), budget)
		defer cancel()

		release := make(chan struct{})
		appr := &fakeApprover{approved: true, entered: make(chan struct{}, 1), block: release}
		ctx := gate.With(base, policyReturning(gate.AskWith(gate.RecallSession)), gate.NewMemGrants(), appr)

		errc := make(chan error, 1)
		go func() { errc <- gate.Check(ctx, testAction, nil) }()

		<-appr.entered // Ask reached — the clock is now paused
		synctest.Wait()

		// Jump synthetic time far past the budget; the paused deadline must not fire.
		time.Sleep(budget * 10)
		synctest.Wait()
		if err := base.Err(); err != nil {
			t.Fatalf("turn clock fired while parked on Ask: %v", err)
		}

		close(release) // human answers
		synctest.Wait()
		if err := <-errc; err != nil {
			t.Fatalf("Check after approval = %v, want nil", err)
		}
		// Resume restored the deadline with its remaining time — ctx is still alive.
		if err := base.Err(); err != nil {
			t.Fatalf("ctx cancelled after resume: %v", err)
		}
	})
}

// TestCheck_TellsTheApproverTheCeiling pins the whole point of the ceiling parameter: an approver
// renders the answer sheet, so it has to know which answers survive. Before this it did not, and
// Check narrowed the human's choice afterwards — a person pressed "always" and was asked again the
// next day, having answered a question that was never on offer.
func TestCheck_TellsTheApproverTheCeiling(t *testing.T) {
	for _, ceiling := range []gate.Recall{gate.RecallNever, gate.RecallSession, gate.RecallAlways} {
		appr := &fakeApprover{approved: true}
		policy := gate.PolicyFunc(func(gate.Action) gate.Ruling { return gate.AskWith(ceiling) })
		ctx := gate.With(t.Context(), policy, gate.NewMemGrants(), appr)

		if err := gate.Check(ctx, gate.Action{Kind: "net", Target: "example.com"}, nil); err != nil {
			t.Fatalf("ceiling %v: Check: %v", ceiling, err)
		}
		if appr.lastCeiling != ceiling {
			t.Errorf("approver was told ceiling %v, want %v", appr.lastCeiling, ceiling)
		}
	}
}

// TestCheck_ClampsAnApproverThatIgnoresTheCeiling pins the belt-and-braces half. Telling the approver
// is about honesty; this is about a third-party implementation that answers above the cap anyway.
func TestCheck_ClampsAnApproverThatIgnoresTheCeiling(t *testing.T) {
	appr := &fakeApprover{approved: true, recall: gate.RecallAlways}
	grants := gate.NewMemGrants()
	policy := gate.PolicyFunc(func(gate.Action) gate.Ruling { return gate.AskWith(gate.RecallNever) })
	ctx := gate.With(t.Context(), policy, grants, appr)

	action := gate.Action{Kind: "net", Target: "example.com"}
	if err := gate.Check(ctx, action, nil); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if grants.Allowed(action, nil) {
		t.Error("an answer above a RecallNever ceiling was remembered anyway")
	}
}

func TestOfferable(t *testing.T) {
	cases := []struct {
		name       string
		want       gate.Recall
		ceiling    gate.Recall
		widens     bool
		wantRecall gate.Recall
		wantOK     bool
	}{
		{"under the ceiling, untouched", gate.RecallSession, gate.RecallAlways, false, gate.RecallSession, true},
		{"at the ceiling, untouched", gate.RecallAlways, gate.RecallAlways, false, gate.RecallAlways, true},
		{"an exact answer above the ceiling is dropped, not clamped", gate.RecallAlways, gate.RecallSession, false, gate.RecallAlways, false},
		{"once survives every ceiling", gate.RecallNever, gate.RecallNever, false, gate.RecallNever, true},
		{"always is dropped under a never ceiling", gate.RecallAlways, gate.RecallNever, false, gate.RecallAlways, false},
		{"a widening is clamped like anything else", gate.RecallAlways, gate.RecallSession, true, gate.RecallSession, true},
		{"a widening that remembers nothing is not an answer", gate.RecallAlways, gate.RecallNever, true, gate.RecallNever, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recall, ok := gate.Offerable(tc.want, tc.ceiling, tc.widens)
			if recall != tc.wantRecall || ok != tc.wantOK {
				t.Errorf("Offerable(%v, %v, widens=%v) = (%v, %v), want (%v, %v)",
					tc.want, tc.ceiling, tc.widens, recall, ok, tc.wantRecall, tc.wantOK)
			}
		})
	}
}

// TestOfferable_AlwaysLeavesAnAnswer pins the property every approver's sheet depends on: whatever
// the ceiling, "allow once" survives. Filtering by a ceiling must narrow the answers, never remove
// them all — an action nobody can approve is not a stricter policy, it is a broken one.
func TestOfferable_AlwaysLeavesAnAnswer(t *testing.T) {
	for _, ceiling := range []gate.Recall{gate.RecallNever, gate.RecallSession, gate.RecallAlways} {
		if _, ok := gate.Offerable(gate.RecallNever, ceiling, false); !ok {
			t.Errorf("ceiling %v left no answer at all", ceiling)
		}
	}
}
