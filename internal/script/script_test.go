package script_test

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/script"
)

// gateGuest stands in for the QuickJS interpreter: a minimal WASI guest that
// passes its stdin verbatim to the host import nocturn.call and writes the
// response to stdout. It exercises the real Runner → sandbox → gate → dispatch
// round-trip without depending on the heavy interpreter build. (The real
// interpreter's JS binding nocturn.call(tool, args) drives the same import.)
//
//go:embed testdata/gate.wasm
var gateGuest []byte

// recordingTool is a fake brain.Tool that captures the args it was invoked with
// and returns a fixed output — enough to prove the gate dispatches correctly.
func recordingTool(name, out string, gotArgs *string) brain.Tool {
	return brain.Tool{
		ToolSpec: brain.ToolSpec{Name: name},
		Invoke: func(_ context.Context, args string) (string, error) {
			if gotArgs != nil {
				*gotArgs = args
			}
			return out, nil
		},
	}
}

// The one gate end to end: a script's nocturn.call(tool, args) reaches the named
// tool's Invoke with its args, and the result flows back out.
func TestRun_GateDispatchesToTool(t *testing.T) {
	var gotArgs string
	r := script.NewWithGuest(gateGuest, brain.NewRegistry([]brain.Tool{recordingTool("echo", "PONG", &gotArgs)}))

	out, err := r.Run(context.Background(), `{"tool":"echo","args":{"v":42}}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "PONG" {
		t.Fatalf("stdout = %q, want %q", out, "PONG")
	}
	if gotArgs != `{"v":42}` {
		t.Fatalf("tool received args %q, want %q", gotArgs, `{"v":42}`)
	}
}

// An unknown tool is fail-closed: the dispatcher returns an error, which the
// sandbox surfaces to the guest as an "error: ..." string (the interpreter's
// binding turns that into a JS exception) — the host never crashes.
func TestRun_UnknownTool_SurfacesErrorToGuest(t *testing.T) {
	r := script.NewWithGuest(gateGuest, nil)

	out, err := r.Run(context.Background(), `{"tool":"nope","args":{}}`)
	if err != nil {
		t.Fatalf("Run should not fail on a guest-visible tool error: %v", err)
	}
	if !strings.HasPrefix(out, "error: ") || !strings.Contains(out, "unknown tool nope") {
		t.Fatalf("stdout = %q, want an error string naming the unknown tool", out)
	}
}

// A tool's own Invoke error reaches the guest the same way (e.g. a denied
// effect: Guard.Authorize → ErrDenied → "error: ...").
func TestRun_ToolError_SurfacesErrorToGuest(t *testing.T) {
	boom := brain.Tool{
		ToolSpec: brain.ToolSpec{Name: "boom"},
		Invoke:   func(context.Context, string) (string, error) { return "", errors.New("kaboom") },
	}
	r := script.NewWithGuest(gateGuest, brain.NewRegistry([]brain.Tool{boom}))

	out, err := r.Run(context.Background(), `{"tool":"boom","args":{}}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "error: kaboom") {
		t.Fatalf("stdout = %q, want it to contain the tool error", out)
	}
}

// The interpreter never dispatches itself: a script calling nocturn.call("code.run", …)
// is refused (no recursive interpreter), surfaced to the guest as a catchable error.
func TestRun_CodeRunNotReentrant(t *testing.T) {
	r := script.NewWithGuest(gateGuest, brain.NewRegistry(nil))

	out, err := r.Run(context.Background(), `{"tool":"code.run","args":{"source":"1"}}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.HasPrefix(out, "error: ") || !strings.Contains(out, "code.run is not callable") {
		t.Fatalf("stdout = %q, want code.run rejected", out)
	}
}

// code.run is the model-facing tool: it takes {"source": "..."} and runs it,
// returning the script's stdout.
func TestTool_CodeRun_RunsSource(t *testing.T) {
	var gotArgs string
	r := script.NewWithGuest(gateGuest, brain.NewRegistry([]brain.Tool{recordingTool("echo", "OK", &gotArgs)}))

	tool := r.Tool()
	if tool.Name != "code.run" {
		t.Fatalf("tool name = %q, want code.run", tool.Name)
	}
	// With the gate guest, the "source" is the gate request its stdin carries.
	args, _ := json.Marshal(map[string]string{"source": `{"tool":"echo","args":{"a":1}}`})
	out, err := tool.Invoke(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if out != "OK" {
		t.Fatalf("out = %q, want OK", out)
	}
	if gotArgs != `{"a":1}` {
		t.Fatalf("tool received args %q", gotArgs)
	}
}

// code.run validates its own arguments (structure + schema guidance + our
// validation), reporting bad input rather than running an empty script.
func TestTool_CodeRun_RejectsMissingSource(t *testing.T) {
	r := script.NewWithGuest(gateGuest, nil)
	tool := r.Tool()

	if _, err := tool.Invoke(context.Background(), `{}`); err == nil {
		t.Fatal("expected an error for missing source, got nil")
	}
	if _, err := tool.Invoke(context.Background(), `not json`); err == nil {
		t.Fatal("expected an error for invalid arguments, got nil")
	}
}
