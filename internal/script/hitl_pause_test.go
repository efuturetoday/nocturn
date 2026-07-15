package script_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/netcap"
	"github.com/efuturetoday/nocturn/internal/script"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// delayedApprover plays a slow human: it approves (Allow once) after a fixed
// delay — longer than the script's execution budget.
type delayedApprover struct {
	engine *hitl.Engine
	delay  time.Duration
}

func (d *delayedApprover) Notify(_ string, opts []hitl.Option) error {
	go func() {
		time.Sleep(d.delay)
		for _, o := range opts {
			if o.Outcome == hitl.Approved {
				_ = d.engine.Resolve(o.Token)
				return
			}
		}
	}()
	return nil
}

// The core requirement: a script triggers an out-of-band approval that takes
// LONGER than the sandbox execution budget. Because hitl pauses the budget while
// waiting for the human, the guest is not trapped — after approval the effect
// runs and the script completes. (Uses the fast WAT gate guest so the short
// budget isn't eaten by QuickJS materialization.)
func TestHITL_ApprovalOutlastsBudget_ScriptStillCompletes(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte("written-ok"))
	}))
	defer srv.Close()

	approver := &delayedApprover{delay: 400 * time.Millisecond}
	engine := hitl.NewEngine([]byte("test-host-key"), approver)
	approver.engine = engine

	netCap := &netcap.Net{
		Guard: &gateway.Guard{
			Policy: capability.Policy{Rules: []capability.Rule{
				{Capability: "http.write", HostGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
			}},
			Approvals: engine,
			Epochs:    capability.NewEpochRegistry(),
			TTL:       3 * time.Second,
		},
		Scanner: secret.NewScanner(secret.NewStore()),
		HTTP:    srv.Client(),
	}

	r := script.NewWithGuest(gateGuest, tool.NewRegistry(netCap.Tools()))
	r.Timeout = 200 * time.Millisecond // shorter than the 400ms approval

	// gate guest: stdin IS the gate request.
	req := `{"tool":"http.write","args":{"url":` + jsString(srv.URL) + `,"body":"x"}}`
	out, err := r.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run failed — the HITL wait must not trap the guest: %v", err)
	}
	if !strings.Contains(out, "written-ok") {
		t.Fatalf("stdout = %q, want the server response after approval", out)
	}
	if hits != 1 {
		t.Fatalf("server hit %d times, want exactly 1 (after approval)", hits)
	}
}
