// Package spike is a THROWAWAY evaluation of Extism as the ABI/PDK layer.
// It lives in its own Go module (see go.mod) so its dependencies never touch
// the real nocturn module. Delete after the adopt/don't-adopt decision.
//
// It answers three questions:
//  1. Does Extism handle the memory ABI (copy/grow safety) for us?  -> we use
//     p.ReadBytes only; no raw (ptr,len), no copy discipline on our side.
//  2. Is OUR Go host function the control point where broker/HITL/epoch sit?
//  3. What is the dependency/TCB cost? (see: go list -m all)
package spike

import (
	"context"
	"strings"
	"testing"

	extism "github.com/extism/go-sdk"
)

func TestSpike_ExtismHostFunctionIsOurControlPoint(t *testing.T) {
	ctx := context.Background()

	// Proves OUR code runs when the guest reaches for the capability.
	var hostFnHit bool

	// hello_world is the guest's imported user host function
	// (import "extism:host/user" "hello_world"). In the real system THIS is
	// where the capability broker (deny>ask>allow), HITL, and epoch checks
	// would live. Note we only call p.ReadBytes — Extism copies the bytes out
	// of guest memory safely; we never touch raw (ptr,len) or worry about
	// memory.grow invalidating a view.
	helloWorld := extism.NewHostFunctionWithStack(
		"hello_world",
		func(_ context.Context, p *extism.CurrentPlugin, stack []uint64) {
			hostFnHit = true
			in, err := p.ReadBytes(stack[0])
			if err != nil {
				t.Errorf("host fn read failed: %v", err)
			}
			t.Logf("[broker] guest invoked capability hello_world payload=%q -> ALLOW", string(in))
			stack[0] = stack[0] // echo the handle back (what the example expects)
		},
		[]extism.ValueType{extism.ValueTypePTR},
		[]extism.ValueType{extism.ValueTypePTR},
	)

	manifest := extism.Manifest{
		Wasm: []extism.Wasm{extism.WasmFile{Path: "testdata/code-functions.wasm"}},
		// Deny-by-default capability surface: nothing granted.
		AllowedHosts: []string{},
		AllowedPaths: map[string]string{},
	}

	plugin, err := extism.NewPlugin(ctx, manifest,
		extism.PluginConfig{EnableWasi: true},
		[]extism.HostFunction{helloWorld})
	if err != nil {
		t.Fatalf("failed to init plugin: %v", err)
	}
	defer plugin.Close(ctx)

	exit, out, err := plugin.Call("count_vowels", []byte("Hello, World!"))
	if err != nil {
		t.Fatalf("call failed (exit=%d): %v", exit, err)
	}

	t.Logf("guest returned: %s", string(out))
	if !hostFnHit {
		t.Fatal("our host function was NOT reached — guest bypassed the control point")
	}
	if !strings.Contains(string(out), "count") {
		t.Fatalf("unexpected guest output: %q", string(out))
	}
}
