// Package host runs untrusted WebAssembly guests under strict isolation.
//
// This is the innermost layer of Nocturn: the boundary between trusted host
// code and an untrusted guest. The single invariant established here is
// ZERO AMBIENT AUTHORITY — a guest can reach nothing (no network, no files,
// no clock, no syscalls) unless the host explicitly hands it a capability.
// Nothing is granted here, so nothing is possible here.
package host

import (
	"context"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// Run compiles and instantiates a WebAssembly guest with zero ambient
// authority: no WASI, no host functions, nothing.
//
// Because wazero grants a guest no capabilities by default, a guest that
// needs any import (e.g. a wasip1 program that wants to write to stdout)
// cannot be satisfied and fails to instantiate. Denial is structural — the
// capability is unforgeable by its absence — not a runtime check that could
// be bypassed.
func Run(ctx context.Context, guest []byte) error {
	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)

	_, err := r.InstantiateWithConfig(ctx, guest, wazero.NewModuleConfig())
	return err
}

// LogSink receives text a guest sends out through the log window.
type LogSink func(text string)

// RunWithLog runs a guest granted exactly ONE capability: a host function
// nocturn.log(ptr, len) through which it can send text to the host. This is
// the first narrow window over the zero-authority core (see Run): the guest
// still has no network, files, or clock — only this one explicitly granted
// function exists in its world.
//
// The memory ABI: WASM calls can only pass numbers, so the guest writes its
// bytes into its own linear memory and passes (ptr, len). The host reads
// exactly len bytes at ptr from that same memory. Reads are bounds-checked by
// wazero, so a guest cannot trick the host into reading outside its sandbox.
func RunWithLog(ctx context.Context, guest []byte, sink LogSink) error {
	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)

	_, err := r.NewHostModuleBuilder("nocturn").
		NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, ptr, length uint32) {
			buf, ok := mod.Memory().Read(ptr, length)
			if !ok {
				// Out-of-bounds (ptr, len): the guest pointed outside its own
				// memory. Deny by doing nothing; the sandbox held.
				return
			}
			sink(string(buf))
		}).
		Export("log").
		Instantiate(ctx)
	if err != nil {
		return err
	}

	_, err = r.InstantiateWithConfig(ctx, guest, wazero.NewModuleConfig())
	return err
}

// RunWithHITLLog wires the whole stack together for the first time: the log
// window is guarded by a Policy, and when the broker returns Ask the call is
// escalated out of band to the human-in-the-loop engine. The effect happens
// only if a human approves; Deny, denial, or timeout drop it. Guest asks ->
// broker gates -> human decides on a second device -> effect follows.
//
// The guest is suspended inside the host function while the request is pending
// (queue-then-execute); this is intended.
func RunWithHITLLog(ctx context.Context, guest []byte, policy capability.Policy, engine *hitl.Engine, ttl time.Duration, sink LogSink) error {
	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)

	_, err := r.NewHostModuleBuilder("nocturn").
		NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module, ptr, length uint32) {
			buf, ok := mod.Memory().Read(ptr, length)
			if !ok {
				return
			}
			text := string(buf) // copy out before deciding (memory-safety)

			switch policy.Evaluate(capability.Call{Capability: "log"}, capability.Env{}) {
			case capability.Allow:
				sink(text)
			case capability.Ask:
				choices := []hitl.Choice{
					{Label: "Allow", Outcome: hitl.Approved},
					{Label: "Deny", Outcome: hitl.Denied},
				}
				if out, _ := engine.Request(ctx, "log: "+text, choices, ttl); out == hitl.Approved {
					sink(text)
				}
			default:
				// Deny: no effect.
			}
		}).
		Export("log").
		Instantiate(ctx)
	if err != nil {
		return err
	}

	_, err = r.InstantiateWithConfig(ctx, guest, wazero.NewModuleConfig())
	return err
}

// RunWithBrokeredLog is RunWithLog with the log window guarded by a Policy.
// Before the guest's text is delivered to the sink, the broker evaluates the
// call:
//
//	Allow -> deliver to sink
//	Deny  -> drop; the effect does not happen
//	Ask   -> treated as Deny for now. Out-of-band human approval is a later
//	         layer (HITL); until it exists, safe-by-default means no effect
//	         happens without an explicit Allow.
//
// The guest still runs to completion in every case; only the effect is gated.
func RunWithBrokeredLog(ctx context.Context, guest []byte, policy capability.Policy, sink LogSink) error {
	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)

	_, err := r.NewHostModuleBuilder("nocturn").
		NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, ptr, length uint32) {
			if policy.Evaluate(capability.Call{Capability: "log"}, capability.Env{}) != capability.Allow {
				return // Deny or Ask: no effect.
			}
			buf, ok := mod.Memory().Read(ptr, length)
			if !ok {
				return
			}
			sink(string(buf))
		}).
		Export("log").
		Instantiate(ctx)
	if err != nil {
		return err
	}

	_, err = r.InstantiateWithConfig(ctx, guest, wazero.NewModuleConfig())
	return err
}
