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
	out, err := gateway.Do(context.Background(), g, probeCall, "probe example.com", gateway.WithoutScan(), func() (string, error) {
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

// allowProbe permits the probe call (used by the EgressScan tests below).
func allowProbe() *gateway.Guard {
	return &gateway.Guard{Policy: capability.Policy{Rules: []capability.Rule{
		{Family: "probe", TargetGlob: capability.Wildcard, Writes: capability.MatchAny, Effect: capability.Allow, Epoch: capability.Permanent},
	}}}
}

// blockScanner is a fake EgressScanner that blocks any part it was told to.
type blockScanner struct{ bad string }

func (b blockScanner) ScanEgress(parts ...string) error {
	for _, p := range parts {
		if strings.Contains(p, b.bad) {
			return errors.New("leak blocked")
		}
	}
	return nil
}

// A zero-value EgressScan (neither ScanEgress nor WithoutScan chosen) fails closed:
// the effect never runs, so a new capability cannot silently ship unscanned.
func TestDo_ScanUnspecified_FailsClosed(t *testing.T) {
	ran := false
	_, err := gateway.Do(context.Background(), allowProbe(), probeCall, "probe", gateway.EgressScan{}, func() (string, error) {
		ran = true
		return "x", nil
	})
	if !errors.Is(err, gateway.ErrScanUnspecified) {
		t.Fatalf("err = %v, want ErrScanUnspecified", err)
	}
	if ran {
		t.Fatal("effect ran with no egress-scan decision")
	}
}

// An external effect that declares ScanEgress but produces an EMPTY egress surface
// fails closed — the caller forgot to extract the outbound bytes.
func TestDo_EmptyEgress_FailsClosed(t *testing.T) {
	for _, empty := range [][]string{nil, {}, {""}, {"  "}} {
		ran := false
		_, err := gateway.Do(context.Background(), allowProbe(), probeCall, "probe",
			gateway.ScanEgress(blockScanner{}, func() []string { return empty }),
			func() (string, error) { ran = true; return "x", nil })
		if !errors.Is(err, gateway.ErrEmptyEgress) {
			t.Fatalf("parts %q: err = %v, want ErrEmptyEgress", empty, err)
		}
		if ran {
			t.Fatalf("parts %q: effect ran despite empty egress", empty)
		}
	}
}

// A leak in the egress surface blocks the effect; a clean surface lets it run.
func TestDo_ScanEgress_BlocksLeakAllowsClean(t *testing.T) {
	sc := blockScanner{bad: "SECRET"}

	ran := false
	_, err := gateway.Do(context.Background(), allowProbe(), probeCall, "probe",
		gateway.ScanEgress(sc, func() []string { return []string{"https://x/?t=SECRET"} }),
		func() (string, error) { ran = true; return "x", nil })
	if err == nil || ran {
		t.Fatalf("leaking egress: err=%v ran=%v, want a block and no effect", err, ran)
	}

	ran = false
	out, err := gateway.Do(context.Background(), allowProbe(), probeCall, "probe",
		gateway.ScanEgress(sc, func() []string { return []string{"https://x/clean"} }),
		func() (string, error) { ran = true; return "ok", nil })
	if err != nil || !ran || out != "ok" {
		t.Fatalf("clean egress: out=%q ran=%v err=%v, want the effect to run", out, ran, err)
	}
}

// WithoutScan runs a local effect with no scan — and never touches the scanner.
func TestDo_WithoutScan_SkipsScan(t *testing.T) {
	ran := false
	out, err := gateway.Do(context.Background(), allowProbe(), probeCall, "probe", gateway.WithoutScan(),
		func() (string, error) { ran = true; return "local", nil })
	if err != nil || !ran || out != "local" {
		t.Fatalf("out=%q ran=%v err=%v, want the local effect to run unscanned", out, ran, err)
	}
}

// A denied external call is blocked BEFORE the egress surface is even built — the
// scan closure must not run on a denied call (authorize wins first).
func TestDo_DeniedBeforeEgress(t *testing.T) {
	deny := &gateway.Guard{Policy: capability.Policy{}} // deny-by-default
	built := false
	_, err := gateway.Do(context.Background(), deny, probeCall, "probe",
		gateway.ScanEgress(blockScanner{}, func() []string { built = true; return []string{"x"} }),
		func() (string, error) { return "x", nil })
	if !errors.Is(err, gateway.ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
	if built {
		t.Fatal("egress surface was built on a denied call — it should never run")
	}
}

// On a denied call (empty policy = deny-by-default) Do returns ErrDenied and the
// effect is UNREACHABLE — it must never run, and the result is the zero value.
func TestDo_DeniedNeverRunsEffect(t *testing.T) {
	g := &gateway.Guard{Policy: capability.Policy{}} // no rule matches → deny

	ran := false
	out, err := gateway.Do(context.Background(), g, probeCall, "probe example.com", gateway.WithoutScan(), func() (string, error) {
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
