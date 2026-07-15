package gateway_test

import (
	"context"
	"errors"
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
			{Capability: "http.read", TargetGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
		}},
		Approvals: eng,
		TTL:       time.Second,
	}
	return g, n
}

func read(host string) capability.Call {
	return capability.Call{Capability: "http.read", Target: host}
}

// An effect outside the ceiling chain is hard-denied — the human is NEVER asked
// (the anti-prompt-injection rail).
func TestAuthorize_OutsideCeiling_HardDeniesWithoutAsking(t *testing.T) {
	g, n := askGuard(t, hitl.Approved)
	ceiling := capability.NewCeiling(capability.Pair{Capability: "http.read", TargetGlob: "good.com"})
	ctx := capability.WithCeiling(context.Background(), ceiling)

	if err := g.Authorize(ctx, read("evil.com"), "GET evil"); !errors.Is(err, gateway.ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
	if n.calls != 0 {
		t.Fatalf("human asked %d times for an out-of-ceiling call, want 0", n.calls)
	}
	// A call inside the ceiling still reaches HITL and is allowed once.
	if err := g.Authorize(ctx, read("good.com"), "GET good"); err != nil {
		t.Fatalf("in-ceiling call: %v", err)
	}
	if n.calls != 1 {
		t.Fatalf("in-ceiling call asked %d times, want 1", n.calls)
	}
}

// memGrants is an in-memory PersistentGrants for asserting "always" persistence.
type memGrants struct{ recs map[string]bool }

func (m *memGrants) key(ctx string, c capability.Call) string {
	return ctx + "|" + c.Capability + "|" + c.Target
}
func (m *memGrants) Allows(ctx string, c capability.Call) bool { return m.recs[m.key(ctx, c)] }
func (m *memGrants) Record(ctx string, c capability.Call) error {
	m.recs[m.key(ctx, c)] = true
	return nil
}

// "Allow always" records a grant on the context's durable store; a FRESH context
// backed by the same store then allows silently — the grant survived the context.
func TestAuthorize_ApprovedAlways_PersistsAcrossContexts(t *testing.T) {
	g, n := askGuard(t, hitl.ApprovedAlways)
	store := &memGrants{recs: map[string]bool{}}

	ctx1 := capability.WithGrants(context.Background(), capability.NewGrants("ws", capability.Permanent, store))
	if err := g.Authorize(ctx1, read("api.example.com"), "GET"); err != nil {
		t.Fatalf("first authorize: %v", err)
	}
	if n.calls != 1 {
		t.Fatalf("asked %d times, want 1", n.calls)
	}

	// A fresh context (new epoch) with the same store: no ask, the always-grant holds.
	ctx2 := capability.WithGrants(context.Background(), capability.NewGrants("ws", capability.Permanent, store))
	if err := g.Authorize(ctx2, read("api.example.com"), "GET"); err != nil {
		t.Fatalf("second authorize: %v", err)
	}
	if n.calls != 1 {
		t.Fatalf("asked %d times total, want 1 (always-grant must survive the context)", n.calls)
	}
}
