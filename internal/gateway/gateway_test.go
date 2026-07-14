package gateway_test

import (
	"context"
	"errors"
	"testing"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
)

var probeCall = capability.Call{Capability: "probe", Attrs: map[string]string{"host": "example.com"}}

// Do runs the effect only when the call is allowed, and returns its result.
func TestDo_AllowedRunsEffect(t *testing.T) {
	g := &gateway.Guard{Policy: capability.Policy{Rules: []capability.Rule{
		{Capability: "probe", HostGlob: capability.Wildcard, Effect: capability.Allow, Epoch: capability.Permanent},
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
