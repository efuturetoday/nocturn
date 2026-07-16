package capability_test

import (
	"testing"

	"github.com/efuturetoday/nocturn/internal/capability"
)

func TestEpochRegistry_OpenCloseIsAlive(t *testing.T) {
	reg := capability.NewEpochRegistry()
	e1 := reg.Open()
	e2 := reg.Open()
	if e1 == 0 || e2 == 0 || e1 == e2 {
		t.Fatalf("Open must mint distinct non-zero ids, got e1=%d e2=%d", e1, e2)
	}
	if !reg.IsAlive(e1) || !reg.IsAlive(e2) {
		t.Fatal("freshly opened epochs must be alive")
	}
	reg.Close(e1)
	if reg.IsAlive(e1) {
		t.Fatal("closed epoch must not be alive")
	}
	if !reg.IsAlive(e2) {
		t.Fatal("closing one epoch must not affect another")
	}
}

// The core property: a grant bound to an epoch works while the epoch is alive
// and dies the instant it is revoked — a later (stale/injected) reuse is denied.
func TestEvaluate_RevocationKillsGrant(t *testing.T) {
	reg := capability.NewEpochRegistry()
	epoch := reg.Open()

	policy := capability.Policy{Rules: []capability.Rule{
		{Family: "pay", Writes: capability.MatchAny, Effect: capability.Allow, Epoch: epoch},
	}}
	call := capability.Call{Family: "pay"}

	if got := policy.Evaluate(call, capability.Env{Epochs: reg}); got != capability.Allow {
		t.Fatalf("within a live epoch: got %v, want Allow", got)
	}

	reg.Close(epoch) // task done — revoke

	if got := policy.Evaluate(call, capability.Env{Epochs: reg}); got != capability.Deny {
		t.Fatalf("after revocation (stale replay): got %v, want Deny", got)
	}
}

// The zero value is unset and fails closed: a grant with no Epoch matches
// nothing, so a forgotten field never silently grants lasting authority.
// Permanence must be requested explicitly with Permanent.
func TestUnsetEpoch_FailsClosed(t *testing.T) {
	reg := capability.NewEpochRegistry()
	policy := capability.Policy{Rules: []capability.Rule{
		{Family: "log", Writes: capability.MatchAny, Effect: capability.Allow}, // Epoch left unset (zero)
	}}
	call := capability.Call{Family: "log"}
	if got := policy.Evaluate(call, capability.Env{}); got != capability.Deny {
		t.Fatalf("unset epoch under Evaluate: got %v, want Deny", got)
	}
	if got := policy.Evaluate(call, capability.Env{Epochs: reg}); got != capability.Deny {
		t.Fatalf("unset epoch with a registry: got %v, want Deny", got)
	}
}

// A permanent grant (Epoch: Permanent) is honoured regardless of epoch context.
func TestPermanentGrant_UnaffectedByEpochs(t *testing.T) {
	reg := capability.NewEpochRegistry()
	policy := capability.Policy{Rules: []capability.Rule{
		{Family: "log", Writes: capability.MatchAny, Effect: capability.Allow, Epoch: capability.Permanent},
	}}
	call := capability.Call{Family: "log"}

	if got := policy.Evaluate(call, capability.Env{}); got != capability.Allow {
		t.Fatalf("Evaluate: permanent grant got %v, want Allow", got)
	}
	if got := policy.Evaluate(call, capability.Env{Epochs: reg}); got != capability.Allow {
		t.Fatalf("with a registry: permanent grant got %v, want Allow", got)
	}
}

// Fail closed: an epoch-scoped grant evaluated with an empty Env (no registry)
// is treated as dead — you must pass Env{Epochs: reg} to honour a live epoch,
// so forgetting the registry never silently grants authority.
func TestEpochScopedGrant_FailsClosedWithoutRegistry(t *testing.T) {
	policy := capability.Policy{Rules: []capability.Rule{
		{Family: "pay", Writes: capability.MatchAny, Effect: capability.Allow, Epoch: 42},
	}}
	if got := policy.Evaluate(capability.Call{Family: "pay"}, capability.Env{}); got != capability.Deny {
		t.Fatalf("epoch-scoped grant under plain Evaluate: got %v, want Deny", got)
	}
}

// Revoking one epoch does not affect a grant bound to a different, live epoch.
func TestRevocation_IsolatedPerEpoch(t *testing.T) {
	reg := capability.NewEpochRegistry()
	booking := reg.Open()
	research := reg.Open()

	policy := capability.Policy{Rules: []capability.Rule{
		{Family: "pay", Writes: capability.MatchAny, Effect: capability.Allow, Epoch: booking},
		{Family: "http", TargetGlob: capability.Wildcard, Writes: capability.MatchAny, Effect: capability.Allow, Epoch: research},
	}}

	reg.Close(booking)

	if got := policy.Evaluate(capability.Call{Family: "pay"}, capability.Env{Epochs: reg}); got != capability.Deny {
		t.Fatalf("revoked epoch grant: got %v, want Deny", got)
	}
	fetch := capability.Call{Family: "http", Target: "example.com"}
	if got := policy.Evaluate(fetch, capability.Env{Epochs: reg}); got != capability.Allow {
		t.Fatalf("still-live epoch grant: got %v, want Allow", got)
	}
}
