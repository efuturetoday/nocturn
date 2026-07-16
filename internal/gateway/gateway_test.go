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

var probeCall = capability.Call{Family: "probe", Target: "example.com"}

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

// A trusted layer's ctx intent (WithIntent) is shown to the human as the semantic
// HEAD, with the host-computed fact line always riding beneath it (never replaced)
// so a template can't hide the real (capability, target). Without a ctx intent, the
// tool's own transport-level default is used as-is.
func TestAuthorize_WithIntentOverridesPrompt(t *testing.T) {
	notifier := &captureNotifier{}
	engine := hitl.NewEngine([]byte("k"), notifier)
	notifier.resolve = engine.Resolve
	g := &gateway.Guard{
		Policy: capability.Policy{Rules: []capability.Rule{
			{Family: "probe", TargetGlob: capability.Wildcard, Writes: capability.MatchAny, Effect: capability.Ask, Epoch: capability.Permanent},
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
	// Head = the semantic intent; a fact line naming the real (capability, target)
	// rides beneath it so the wording can't hide what is gated.
	head, fact, ok := strings.Cut(notifier.intent, "\n")
	if !ok || head != "Send email to x@a" {
		t.Fatalf("prompt head = %q, want the ctx-supplied semantic intent as the first line", notifier.intent)
	}
	if !strings.Contains(fact, probeCall.Family) || !strings.Contains(fact, probeCall.Target) {
		t.Fatalf("fact line = %q, want it to name the real family %q and target %q", fact, probeCall.Family, probeCall.Target)
	}
}

// Do runs the effect only when the call is allowed, and returns its result.
func TestDo_AllowedRunsEffect(t *testing.T) {
	g := &gateway.Guard{Policy: capability.Policy{Rules: []capability.Rule{
		{Family: "probe", TargetGlob: capability.Wildcard, Writes: capability.MatchAny, Effect: capability.Allow, Epoch: capability.Permanent},
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
