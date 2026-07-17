package gateway_test

import (
	"context"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/hitl"
)

// A scope's session grant is honoured while the scope is live and denied once it is
// revoked — all without the caller ever touching an EpochRegistry (the Guard owns it).
// This is the encapsulation the Scope type guarantees: the epoch the Guard checks for
// liveness is exactly the one Revoke closes.
func TestScope_BindThenRevoke_RevokesSessionGrant(t *testing.T) {
	// A base policy that asks on a read, so a session grant is what makes a repeat
	// call silent — and revoking it makes the next call ask again.
	askRead := capability.Policy{Rules: []capability.Rule{
		{Family: "http", TargetGlob: capability.Wildcard, Writes: capability.MatchRead, Effect: capability.Ask, Epoch: capability.Permanent},
	}}
	notifier := &sessionApprover{}
	engine := hitl.NewEngine([]byte("k"), notifier)
	notifier.resolve = engine.Resolve

	g := &gateway.Guard{Policy: askRead, Approvals: engine, TTL: time.Second}
	scope := g.NewScope(nil)
	ctx := scope.Bind(context.Background())

	call := capability.Call{Family: "http", Target: "example.com", Write: false}

	// First call: asked, ApprovedSession recorded on the scope's grants.
	if err := g.Authorize(ctx, call, "read example.com"); err != nil {
		t.Fatalf("first authorize: %v", err)
	}
	// Second call: the live session grant answers it — no ask.
	if err := g.Authorize(ctx, call, "read example.com"); err != nil {
		t.Fatalf("second authorize: %v", err)
	}
	if notifier.calls != 1 {
		t.Fatalf("asked %d times, want 1 (the session grant should cover the 2nd call)", notifier.calls)
	}

	// Revoke the scope: the grant dies with its epoch, so the next call asks again.
	scope.Revoke()
	if err := g.Authorize(ctx, call, "read example.com"); err != nil {
		t.Fatalf("post-revoke authorize: %v", err)
	}
	if notifier.calls != 2 {
		t.Fatalf("asked %d times total, want 2 (Revoke must drop the session grant)", notifier.calls)
	}
}

// A fresh scope from the same Guard does not inherit the previous scope's session
// grant — each scope is its own revocable unit over the guard's registry.
func TestScope_FreshScope_DoesNotInheritGrant(t *testing.T) {
	askRead := capability.Policy{Rules: []capability.Rule{
		{Family: "http", TargetGlob: capability.Wildcard, Writes: capability.MatchRead, Effect: capability.Ask, Epoch: capability.Permanent},
	}}
	notifier := &sessionApprover{}
	engine := hitl.NewEngine([]byte("k"), notifier)
	notifier.resolve = engine.Resolve

	g := &gateway.Guard{Policy: askRead, Approvals: engine, TTL: time.Second}
	call := capability.Call{Family: "http", Target: "example.com"}

	s1 := g.NewScope(nil)
	if err := g.Authorize(s1.Bind(context.Background()), call, "read"); err != nil {
		t.Fatalf("s1 authorize: %v", err)
	}
	s1.Revoke()

	// A brand-new scope must ask again (it carries none of s1's grants).
	s2 := g.NewScope(nil)
	if err := g.Authorize(s2.Bind(context.Background()), call, "read"); err != nil {
		t.Fatalf("s2 authorize: %v", err)
	}
	if notifier.calls != 2 {
		t.Fatalf("asked %d times, want 2 (a fresh scope inherits no grant)", notifier.calls)
	}
}

// sessionApprover resolves each pending request with "Allow this session".
type sessionApprover struct {
	resolve func(token string) error
	calls   int
}

func (n *sessionApprover) Notify(_ string, options []hitl.Option) error {
	n.calls++
	for _, o := range options {
		if o.Outcome == hitl.ApprovedSession {
			return n.resolve(o.Token)
		}
	}
	return nil
}
