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

// The autonomy dial resolves an Ask on an UNATTENDED run without a live human:
// strict denies, full auto-allows, guarded/attended still ask out of band, and a
// consequential effect always asks even under full (the never-auto floor wins).
func TestAuthorize_AutonomyDial(t *testing.T) {
	cases := []struct {
		name          string
		autonomy      capability.Autonomy
		consequential bool
		wantErr       bool
		wantAsks      int
	}{
		{"attended asks", capability.AutonomyAttended, false, false, 1},
		{"guarded asks (phone)", capability.AutonomyGuarded, false, false, 1},
		{"strict denies without asking", capability.AutonomyStrict, false, true, 0},
		{"full auto-allows silently", capability.AutonomyFull, false, false, 0},
		{"full + consequential still asks", capability.AutonomyFull, true, false, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, n := askGuard(t, hitl.Approved)
			ctx := capability.WithAutonomy(context.Background(), c.autonomy)
			if c.consequential {
				ctx = gateway.WithConsequential(ctx)
			}
			err := g.Authorize(ctx, read("api.example.com"), "read api")
			if c.wantErr && !errors.Is(err, gateway.ErrDenied) {
				t.Fatalf("err = %v, want ErrDenied", err)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if n.calls != c.wantAsks {
				t.Fatalf("asked %d times, want %d", n.calls, c.wantAsks)
			}
		})
	}
}

// An unattended run's Ask goes to the OUT-OF-BAND channel (phone); an attended run
// stays on the interactive channel. A missing OOB channel falls back to interactive.
func TestAuthorize_OOBRoutesUnattended(t *testing.T) {
	askPol := capability.Policy{Rules: []capability.Rule{
		{Family: "http", TargetGlob: capability.Wildcard, Writes: capability.MatchRead, Effect: capability.Ask, Epoch: capability.Permanent},
	}}
	newEng := func() (*hitl.Engine, *countNotifier) {
		n := &countNotifier{want: hitl.Approved}
		e := hitl.NewEngine([]byte("k"), n)
		n.resolve = e.Resolve
		return e, n
	}

	t.Run("attended → interactive, guarded → OOB", func(t *testing.T) {
		tuiEng, tui := newEng()
		oobEng, oob := newEng()
		g := &gateway.Guard{Policy: askPol, Approvals: tuiEng, ApprovalsOOB: oobEng, TTL: time.Second}

		if err := g.Authorize(context.Background(), read("x.com"), "r"); err != nil {
			t.Fatalf("attended: %v", err)
		}
		if tui.calls != 1 || oob.calls != 0 {
			t.Fatalf("attended routed wrong: tui=%d oob=%d, want 1/0", tui.calls, oob.calls)
		}

		ctx := capability.WithAutonomy(context.Background(), capability.AutonomyGuarded)
		if err := g.Authorize(ctx, read("x.com"), "r"); err != nil {
			t.Fatalf("guarded: %v", err)
		}
		if oob.calls != 1 || tui.calls != 1 {
			t.Fatalf("guarded routed wrong: tui=%d oob=%d, want tui unchanged, oob 1", tui.calls, oob.calls)
		}
	})

	t.Run("no OOB channel → guarded falls back to interactive", func(t *testing.T) {
		tuiEng, tui := newEng()
		g := &gateway.Guard{Policy: askPol, Approvals: tuiEng, TTL: time.Second} // ApprovalsOOB nil
		ctx := capability.WithAutonomy(context.Background(), capability.AutonomyGuarded)
		if err := g.Authorize(ctx, read("x.com"), "r"); err != nil {
			t.Fatalf("fallback: %v", err)
		}
		if tui.calls != 1 {
			t.Fatalf("fallback: interactive asked %d times, want 1", tui.calls)
		}
	})
}

// The dial never overrides the cage: an unattended full run still hard-denies an
// out-of-cage effect WITHOUT auto-allowing it (the cage ran before the Ask branch).
func TestAuthorize_AutonomyFullStillCaged(t *testing.T) {
	g, n := askGuard(t, hitl.Approved)
	cage := capability.NewCage(capability.Pair{Family: "http", TargetGlob: "good.com", Writes: capability.MatchRead})
	ctx := capability.WithCage(context.Background(), cage)
	ctx = capability.WithAutonomy(ctx, capability.AutonomyFull)

	if err := g.Authorize(ctx, read("evil.com"), "read evil"); !errors.Is(err, gateway.ErrDenied) {
		t.Fatalf("out-of-cage under full autonomy: err = %v, want ErrDenied", err)
	}
	if n.calls != 0 {
		t.Fatalf("out-of-cage asked %d times, want 0", n.calls)
	}
}
