package gateway_test

import (
	"context"
	"errors"
	"testing"

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
