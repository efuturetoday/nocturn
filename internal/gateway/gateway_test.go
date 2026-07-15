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

var probeCall = capability.Call{Capability: "probe", Target: "example.com"}

// captureNotifier records the intent it was prompted with and approves once.
type captureNotifier struct {
	intent  string
	resolve func(string) error
}

func (n *captureNotifier) Notify(intent string, options []hitl.Option) error {
	n.intent = intent
	for _, o := range options {
		if o.Outcome == hitl.Approved {
			return n.resolve(o.Token)
		}
	}
	return errors.New("no approve option")
}

// A trusted layer's ctx intent (WithIntent) is shown to the human instead of the
// effect tool's own transport-level intent; without it, the tool's default is used.
func TestAuthorize_WithIntentOverridesPrompt(t *testing.T) {
	notifier := &captureNotifier{}
	engine := hitl.NewEngine([]byte("k"), notifier)
	notifier.resolve = engine.Resolve
	g := &gateway.Guard{
		Policy: capability.Policy{Rules: []capability.Rule{
			{Capability: "probe", TargetGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
		}},
		Approvals: engine,
		TTL:       time.Second,
	}

	if err := g.Authorize(context.Background(), probeCall, "http.write example.com"); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if notifier.intent != "http.write example.com" {
		t.Fatalf("default intent = %q, want the tool's own", notifier.intent)
	}

	ctx := gateway.WithIntent(context.Background(), "Send email to x@a")
	if err := g.Authorize(ctx, probeCall, "http.write example.com"); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if notifier.intent != "Send email to x@a" {
		t.Fatalf("intent = %q, want the ctx-supplied semantic intent", notifier.intent)
	}
}

// Do runs the effect only when the call is allowed, and returns its result.
func TestDo_AllowedRunsEffect(t *testing.T) {
	g := &gateway.Guard{Policy: capability.Policy{Rules: []capability.Rule{
		{Capability: "probe", TargetGlob: capability.Wildcard, Effect: capability.Allow, Epoch: capability.Permanent},
	}}}

	ran := false
	out, err := gateway.Do(context.Background(), g, probeCall, "probe example.com", func() (string, error) {
		ran = true
		return "effect-ran", nil
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !ran || out != "effect-ran" {
		t.Fatalf("ran=%v out=%q, want the effect to run and its result to return", ran, out)
	}
}

// On a denied call (empty policy = deny-by-default) Do returns ErrDenied and the
// effect is UNREACHABLE — it must never run, and the result is the zero value.
func TestDo_DeniedNeverRunsEffect(t *testing.T) {
	g := &gateway.Guard{Policy: capability.Policy{}} // no rule matches → deny

	ran := false
	out, err := gateway.Do(context.Background(), g, probeCall, "probe example.com", func() (string, error) {
		ran = true
		return "effect-ran", nil
	})
	if !errors.Is(err, gateway.ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
	if ran {
		t.Fatal("the effect ran on a denied call — Do must gate it out")
	}
	if out != "" {
		t.Fatalf("out = %q, want zero value on denial", out)
	}
}
