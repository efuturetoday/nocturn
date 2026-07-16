package gateway_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/hitl"
)

// countNotifier resolves the pending request with a fixed outcome and counts how
// often the human was asked.
type countNotifier struct {
	want    hitl.Outcome
	resolve func(token string) error
	calls   int
}

func (n *countNotifier) Notify(_ string, options []hitl.Option) error {
	n.calls++
	for _, o := range options {
		if o.Outcome == n.want {
			return n.resolve(o.Token)
		}
	}
	return errors.New("countNotifier: no matching option")
}

func askGuard(t *testing.T, want hitl.Outcome) (*gateway.Guard, *countNotifier) {
	t.Helper()
	n := &countNotifier{want: want}
	eng := hitl.NewEngine([]byte("k"), n)
	n.resolve = eng.Resolve
	g := &gateway.Guard{
		Policy: capability.Policy{Rules: []capability.Rule{
			{Family: "http", TargetGlob: capability.Wildcard, Writes: capability.MatchRead, Effect: capability.Ask, Epoch: capability.Permanent},
		}},
		Approvals: eng,
		TTL:       time.Second,
	}
	return g, n
}

func read(host string) capability.Call {
	return capability.Call{Family: "http", Mutates: false, Target: host}
}

func write(host string) capability.Call {
	return capability.Call{Family: "http", Mutates: true, Target: host}
}

// optsNotifier records the outcomes it was offered and resolves with a chosen one.
type optsNotifier struct {
	pick     hitl.Outcome
	resolve  func(string) error
	outcomes []hitl.Outcome
}

func (n *optsNotifier) Notify(_ string, options []hitl.Option) error {
	n.outcomes = nil
	for _, o := range options {
		n.outcomes = append(n.outcomes, o.Outcome)
	}
	for _, o := range options {
		if o.Outcome == n.pick {
			return n.resolve(o.Token)
		}
	}
	return errors.New("optsNotifier: chosen outcome not offered")
}

func writeAskGuard(t *testing.T, n hitl.Notifier) *gateway.Guard {
	t.Helper()
	return &gateway.Guard{
		Policy: capability.Policy{Rules: []capability.Rule{
			{Family: "http", TargetGlob: capability.Wildcard, Writes: capability.MatchWrite, Effect: capability.Ask, Epoch: capability.Permanent},
		}},
		Approvals: hitl.NewEngine([]byte("k"), n),
		TTL:       time.Second,
	}
}

// The never-auto floor: a consequential effect ALWAYS asks out of band — a standing
// grant does not short-circuit it, and only once/deny are offered (never
// session/always), so it can never become a remembered grant.
func TestAuthorize_Consequential_NeverAutoNeverRemembered(t *testing.T) {
	n := &optsNotifier{pick: hitl.Approved}
	g := writeAskGuard(t, n)
	n.resolve = g.Approvals.Resolve

	store := &memGrants{recs: map[string]bool{}}
	grants := capability.NewGrants(capability.Permanent, store)
	// A pre-existing "always" grant that WOULD short-circuit a normal write.
	_ = grants.Record("", write("api.example.com"), capability.ScopeAlways)

	ctx := gateway.WithConsequential(capability.WithGrants(context.Background(), grants))
	if err := g.Authorize(ctx, write("api.example.com"), "delete repo"); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	// It asked despite the standing grant, and offered only once + deny.
	if len(n.outcomes) != 2 || n.outcomes[0] != hitl.Approved || n.outcomes[1] != hitl.Denied {
		t.Fatalf("offered outcomes = %v, want [Approved Denied] (no session/always)", n.outcomes)
	}
}

// A scope (agent) policy composes onto the base by union: a Deny blacklists (deny-
// wins, even over a base Allow), and an Ask tightens a base Allow (ask beats allow).
func TestAuthorize_ScopedPolicyTightens(t *testing.T) {
	n := &optsNotifier{pick: hitl.Approved}
	g := &gateway.Guard{
		Policy: capability.Policy{Rules: []capability.Rule{
			{Family: capability.Wildcard, TargetGlob: capability.Wildcard, Writes: capability.MatchRead, Effect: capability.Allow, Epoch: capability.Permanent},
			{Family: capability.Wildcard, TargetGlob: capability.Wildcard, Writes: capability.MatchWrite, Effect: capability.Ask, Epoch: capability.Permanent},
		}},
		Approvals: hitl.NewEngine([]byte("k"), n),
		TTL:       time.Second,
	}
	n.resolve = g.Approvals.Resolve

	scoped := capability.Policy{Rules: []capability.Rule{
		{Family: "http", TargetGlob: "secret.host", Writes: capability.MatchAny, Effect: capability.Deny, Epoch: capability.Permanent},
		{Family: "http", TargetGlob: "watch.host", Writes: capability.MatchRead, Effect: capability.Ask, Epoch: capability.Permanent},
	}}
	ctx := capability.WithPolicy(context.Background(), scoped)

	// Blacklisted read → hard deny (agent Deny wins over the base read-Allow).
	if err := g.Authorize(ctx, read("secret.host"), "get"); !errors.Is(err, gateway.ErrDenied) {
		t.Fatalf("blacklisted read = %v, want ErrDenied", err)
	}
	// Force-asked read → Ask (agent Ask tightens the base read-Allow), then approved once.
	if err := g.Authorize(ctx, read("watch.host"), "get"); err != nil {
		t.Fatalf("force-asked read: %v", err)
	}
	if len(n.outcomes) == 0 {
		t.Fatal("force-asked read must reach HITL")
	}
	// An ordinary read elsewhere still runs still (base Allow, no scope rule).
	n.outcomes = nil
	if err := g.Authorize(ctx, read("ok.host"), "get"); err != nil {
		t.Fatalf("ordinary read: %v", err)
	}
	if len(n.outcomes) != 0 {
		t.Fatal("an unscoped read must not ask (base Allow)")
	}
}

// A standing grant answers the Ask, but the rate cap still applies on that path —
// a remembered "always" cannot be replayed without bound (regression: the grant
// short-circuit used to bypass the limiter).
func TestAuthorize_StandingGrantRespectsRateCap(t *testing.T) {
	n := &optsNotifier{pick: hitl.Denied} // must not be needed: the grant answers
	g := writeAskGuard(t, n)
	n.resolve = g.Approvals.Resolve
	g.Rate = capability.NewRateLimiter(1, time.Minute)

	grants := capability.NewGrants(capability.Permanent, &memGrants{recs: map[string]bool{}})
	_ = grants.Record("", write("api.example.com"), capability.ScopeAlways)
	ctx := capability.WithGrants(context.Background(), grants)

	if err := g.Authorize(ctx, write("api.example.com"), "post"); err != nil {
		t.Fatalf("first (within budget): %v", err)
	}
	if err := g.Authorize(ctx, write("api.example.com"), "post"); !errors.Is(err, gateway.ErrDenied) {
		t.Fatalf("second (over budget) = %v, want ErrDenied — a grant must still respect the rate cap", err)
	}
}

// An effect outside the cage chain is hard-denied — the human is NEVER asked
// (the anti-prompt-injection rail).
func TestAuthorize_OutsideCage_HardDeniesWithoutAsking(t *testing.T) {
	g, n := askGuard(t, hitl.Approved)
	cage := capability.NewCage(capability.Pair{Family: "http", TargetGlob: "good.com", Writes: capability.MatchRead})
	ctx := capability.WithCage(context.Background(), cage)

	if err := g.Authorize(ctx, read("evil.com"), "GET evil"); !errors.Is(err, gateway.ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
	if n.calls != 0 {
		t.Fatalf("human asked %d times for an out-of-cage call, want 0", n.calls)
	}
	// A call inside the cage still reaches HITL and is allowed once.
	if err := g.Authorize(ctx, read("good.com"), "GET good"); err != nil {
		t.Fatalf("in-cage call: %v", err)
	}
	if n.calls != 1 {
		t.Fatalf("in-cage call asked %d times, want 1", n.calls)
	}
}

// memGrants is an in-memory PersistentGrants for asserting "always" persistence.
type memGrants struct{ recs map[string]bool }

func (m *memGrants) key(tool string, c capability.Call) string {
	return fmt.Sprintf("%s|%s|%v|%s", tool, c.Family, c.Mutates, c.Target)
}
func (m *memGrants) Allows(tool string, c capability.Call) bool { return m.recs[m.key(tool, c)] }
func (m *memGrants) Record(tool string, c capability.Call) error {
	m.recs[m.key(tool, c)] = true
	return nil
}

// "Allow always" records a grant on the context's durable store; a FRESH context
// backed by the same store then allows silently — the grant survived the context.
func TestAuthorize_ApprovedAlways_PersistsAcrossContexts(t *testing.T) {
	g, n := askGuard(t, hitl.ApprovedAlways)
	store := &memGrants{recs: map[string]bool{}}

	ctx1 := capability.WithGrants(context.Background(), capability.NewGrants(capability.Permanent, store))
	if err := g.Authorize(ctx1, read("api.example.com"), "GET"); err != nil {
		t.Fatalf("first authorize: %v", err)
	}
	if n.calls != 1 {
		t.Fatalf("asked %d times, want 1", n.calls)
	}

	// A fresh context (new epoch) with the same store: no ask, the always-grant holds.
	ctx2 := capability.WithGrants(context.Background(), capability.NewGrants(capability.Permanent, store))
	if err := g.Authorize(ctx2, read("api.example.com"), "GET"); err != nil {
		t.Fatalf("second authorize: %v", err)
	}
	if n.calls != 1 {
		t.Fatalf("asked %d times total, want 1 (always-grant must survive the context)", n.calls)
	}
}
