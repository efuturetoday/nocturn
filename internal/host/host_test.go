package host_test

import (
	"bytes"
	"context"
	_ "embed"
	"strings"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/host"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// probeWasm is the untrusted test guest (see testdata/probe/main.go). It tries
// to write to stdout, which under wasip1 requires WASI to be granted.
//
//go:embed testdata/probe.wasm
var probeWasm []byte

// logProbeWasm is a 106-byte guest (see testdata/logprobe/log_probe.wat) that
// imports exactly one host function, nocturn.log, and nothing else.
//
//go:embed testdata/logprobe.wasm
var logProbeWasm []byte

// The core invariant of Nocturn's innermost layer: a guest granted nothing
// cannot act. The probe wants WASI; the host grants none, so it cannot even
// instantiate. Denial is structural, not a bypassable runtime check.
func TestGuest_ZeroAuthority_CannotInstantiate(t *testing.T) {
	err := host.Run(context.Background(), probeWasm)
	if err == nil {
		t.Fatal("guest instantiated with zero authority — isolation is broken")
	}
	t.Logf("guest correctly denied: %v", err)
}

// Positive control: the same guest DOES run once WASI is explicitly granted.
// This proves the failure above is caused by withholding authority, not by an
// unrelated defect in the guest or host.
func TestGuest_WithGrantedWASI_Runs(t *testing.T) {
	ctx := context.Background()
	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)

	wasi_snapshot_preview1.MustInstantiate(ctx, r)

	var out bytes.Buffer
	if _, err := r.InstantiateWithConfig(ctx, probeWasm,
		wazero.NewModuleConfig().WithStdout(&out)); err != nil {
		t.Fatalf("guest failed to run even with WASI granted: %v", err)
	}

	if got := out.String(); !strings.Contains(got, "hello from the guest") {
		t.Fatalf("guest ran but produced unexpected output: %q", got)
	}
}

// Schale 1: with the log window granted, data flows guest -> host through the
// (ptr, len) memory ABI. The host reads the exact bytes the guest points at.
func TestLogWindow_GuestSendsText(t *testing.T) {
	var got string
	err := host.RunWithLog(context.Background(), logProbeWasm, func(text string) {
		got = text
	})
	if err != nil {
		t.Fatalf("guest with granted log window failed to run: %v", err)
	}
	if got != "hello from the guest" {
		t.Fatalf("host received %q through the log window, want %q", got, "hello from the guest")
	}
}

// Schale 0 still holds: the window is opt-in. The very same guest, run without
// the window granted (via Run), cannot instantiate — the imported nocturn.log
// simply does not exist in its world. Absence, not a runtime check, denies it.
func TestLogWindow_NotGranted_CannotInstantiate(t *testing.T) {
	err := host.Run(context.Background(), logProbeWasm)
	if err == nil {
		t.Fatal("guest reached the log window that was never granted — isolation is broken")
	}
	t.Logf("ungranted window correctly absent: %v", err)
}

// Schale 2: the broker gates the window. With an allow policy the effect
// happens; the guest reaches the sink.
func TestBrokeredLog_AllowPolicy_EffectHappens(t *testing.T) {
	policy := capability.Policy{Rules: []capability.Rule{
		{Capability: "log", Effect: capability.Allow, Epoch: capability.Permanent},
	}}
	var got string
	err := host.RunWithBrokeredLog(context.Background(), logProbeWasm, policy, func(text string) {
		got = text
	})
	if err != nil {
		t.Fatalf("guest failed to run: %v", err)
	}
	if got != "hello from the guest" {
		t.Fatalf("allowed log delivered %q, want %q", got, "hello from the guest")
	}
}

// Deny-by-default: an empty policy blocks the effect. The guest still runs to
// completion, but nothing reaches the sink — the window exists but the broker
// refuses the call.
func TestBrokeredLog_DenyByDefault_EffectBlocked(t *testing.T) {
	called := false
	err := host.RunWithBrokeredLog(context.Background(), logProbeWasm, capability.Policy{}, func(string) {
		called = true
	})
	if err != nil {
		t.Fatalf("guest failed to run: %v", err)
	}
	if called {
		t.Fatal("effect happened under an empty (deny-by-default) policy — broker did not gate")
	}
}

// Ask is not performed until HITL exists: safe-by-default means no effect.
func TestBrokeredLog_AskPolicy_EffectBlockedUntilHITL(t *testing.T) {
	policy := capability.Policy{Rules: []capability.Rule{
		{Capability: "log", Effect: capability.Ask, Epoch: capability.Permanent},
	}}
	called := false
	err := host.RunWithBrokeredLog(context.Background(), logProbeWasm, policy, func(string) {
		called = true
	})
	if err != nil {
		t.Fatalf("guest failed to run: %v", err)
	}
	if called {
		t.Fatal("Ask must not perform the effect before out-of-band approval exists")
	}
}

// recordNotifier plays the human: it captures the offered options so the test
// can resolve one of their tokens.
type recordNotifier struct {
	options chan []hitl.Option
}

func (n *recordNotifier) Notify(_ string, options []hitl.Option) error {
	n.options <- options
	return nil
}

func pickToken(opts []hitl.Option, o hitl.Outcome) string {
	for _, opt := range opts {
		if opt.Outcome == o {
			return opt.Token
		}
	}
	return ""
}

func askPolicy() capability.Policy {
	return capability.Policy{Rules: []capability.Rule{
		{Capability: "log", Effect: capability.Ask, Epoch: capability.Permanent},
	}}
}

// End-to-end: guest calls log -> broker says Ask -> out-of-band approval -> the
// effect happens. The whole stack (host + broker + HITL) works together.
func TestHITLLog_Approve_PerformsEffect(t *testing.T) {
	n := &recordNotifier{options: make(chan []hitl.Option, 1)}
	engine := hitl.NewEngine([]byte("test-host-key"), n)

	var got string
	done := make(chan error, 1)
	go func() {
		done <- host.RunWithHITLLog(context.Background(), logProbeWasm, askPolicy(), engine, 2*time.Second, func(text string) {
			got = text
		})
	}()

	opts := <-n.options
	if err := engine.Resolve(pickToken(opts, hitl.Approved)); err != nil {
		t.Fatalf("resolve approve: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != "hello from the guest" {
		t.Fatalf("approved effect not delivered, got %q", got)
	}
}

// End-to-end: the same call, denied out of band -> the effect never happens.
func TestHITLLog_Deny_BlocksEffect(t *testing.T) {
	n := &recordNotifier{options: make(chan []hitl.Option, 1)}
	engine := hitl.NewEngine([]byte("test-host-key"), n)

	called := false
	done := make(chan error, 1)
	go func() {
		done <- host.RunWithHITLLog(context.Background(), logProbeWasm, askPolicy(), engine, 2*time.Second, func(string) {
			called = true
		})
	}()

	opts := <-n.options
	if err := engine.Resolve(pickToken(opts, hitl.Denied)); err != nil {
		t.Fatalf("resolve deny: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	if called {
		t.Fatal("denied call must not perform the effect")
	}
}

