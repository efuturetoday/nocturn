package workspace

import (
	"log/slog"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/tools"
)

// A remembered "yes" for a host outlives the server it was given for: a grant records (Kind, Target)
// and nothing about WHY. So when the thing that prompted the question is removed, the answer stands
// on its own, and the next server declared on that host would inherit a permission nobody gave for
// it. ForgetNetAccess is how the consumer says the reason is gone.
func TestForgetNetAccess(t *testing.T) {
	w, err := Open(Host{LLM: llmStub{}, Log: slog.New(slog.DiscardHandler)}, "test", t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(w.Close)

	const host = "api.example.com"
	action := gate.Action{Kind: tools.NetKind, Target: host}

	w.grants.Remember(gate.Grant{Kind: tools.NetKind, Target: host}, gate.RecallAlways)
	if !w.grants.Allowed(action, nil) {
		t.Fatal("the grant did not take")
	}

	if !w.ForgetNetAccess(host) {
		t.Fatal("ForgetNetAccess did not report the grant it dropped")
	}
	if w.grants.Allowed(action, nil) {
		t.Fatal("the host is still granted")
	}
	// Reporting false the second time is what lets a caller log a revocation only when there was one.
	if w.ForgetNetAccess(host) {
		t.Error("ForgetNetAccess reported dropping a grant that was already gone")
	}

	// Another host's grant is untouched — revoking is per target, not a reset.
	other := "other.example.com"
	w.grants.Remember(gate.Grant{Kind: tools.NetKind, Target: other}, gate.RecallAlways)
	w.ForgetNetAccess(host)
	if !w.grants.Allowed(gate.Action{Kind: tools.NetKind, Target: other}, nil) {
		t.Error("revoking one host took another with it")
	}
}
