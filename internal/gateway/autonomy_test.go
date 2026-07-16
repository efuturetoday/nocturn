package gateway_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/hitl"
)

// promptNotifier records the prompt text it was shown, then approves.
type promptNotifier struct {
	prompt  string
	resolve func(string) error
}

func (n *promptNotifier) Notify(prompt string, options []hitl.Option) error {
	n.prompt = prompt
	for _, o := range options {
		if o.Outcome == hitl.Approved {
			return n.resolve(o.Token)
		}
	}
	return errors.New("promptNotifier: no approve option")
}

// A source label (the workspace of a background run) prefixes the HITL prompt, so a
// human answering out of band knows which context is asking.
func TestAuthorize_WorkspaceLabelPrefixesPrompt(t *testing.T) {
	n := &promptNotifier{}
	eng := hitl.NewEngine([]byte("k"), n)
	n.resolve = eng.Resolve
	g := &gateway.Guard{
		Policy: capability.Policy{Rules: []capability.Rule{
			{Family: "http", TargetGlob: capability.Wildcard, Writes: capability.MatchRead, Effect: capability.Ask, Epoch: capability.Permanent},
		}},
		Approvals: eng,
		TTL:       time.Second,
	}
	ctx := gateway.WithLabel(capability.WithAutonomy(context.Background(), capability.AutonomyGuarded), "work")
	if err := g.Authorize(ctx, read("api.example.com"), "read api"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(n.prompt, "[work] ") {
		t.Fatalf("prompt = %q, want a [work] prefix", n.prompt)
	}
}

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
