package script_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/script"
)

// recordingTool is a fake agentkit.Tool that captures the args it was called with
// and returns a fixed output — enough to prove the gate dispatches correctly.
func recordingTool(t *testing.T, name, out string, gotArgs *string) agentkit.Tool {
	t.Helper()
	tl, err := agentkit.NewTool(name, "desc",
		func(_ context.Context, args string) (string, error) {
			if gotArgs != nil {
				*gotArgs = args
			}
			return out, nil
		})
	if err != nil {
		t.Fatalf("NewTool(%q): %v", name, err)
	}
	return tl
}

// toolset builds an agentkit.ToolSet from the given tools, failing the test on a
// bad/colliding spec.
func toolset(t *testing.T, tools ...agentkit.Tool) agentkit.ToolSet {
	t.Helper()
	ts, err := agentkit.NewToolSet(tools...)
	if err != nil {
		t.Fatalf("NewToolSet: %v", err)
	}
	return ts
}

// requireInterpreter skips the test if the embedded QuickJS interpreter cannot be
// compiled (a build-environment problem, not a behavior under test).
func requireInterpreter(t *testing.T) {
	t.Helper()
	if _, err := script.Engine(); err != nil {
		t.Skipf("QuickJS interpreter engine unavailable: %v", err)
	}
}

// Run returns exactly what the script printed to stdout.
func TestRunner_Run_ReturnsStdout(t *testing.T) {
	requireInterpreter(t)
	r := script.New(nil)

	out, err := r.Run(context.Background(), `console.log("hello from js")`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(out) != "hello from js" {
		t.Fatalf("stdout = %q, want %q", out, "hello from js")
	}
}

// An uncaught top-level throw exits non-zero; Run returns an error carrying the
// guest's stderr, so the failure reason is not swallowed.
func TestRunner_Run_TrapSurfacesStderr(t *testing.T) {
	requireInterpreter(t)
	r := script.New(nil)

	out, err := r.Run(context.Background(), `throw new Error("kaboom");`)
	if err == nil {
		t.Fatalf("expected an error for an uncaught throw, got out=%q", out)
	}
	if !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("err = %v, want it to carry the guest stderr (kaboom)", err)
	}
}

// A runaway script is trapped by the sandbox's wall-clock deadline (wazero has no
// fuel — CPU is bounded by the deadline + memory cap).
func TestRunner_Run_RunawayTrapped(t *testing.T) {
	requireInterpreter(t)
	r := script.New(nil)
	r.Timeout = 300 * time.Millisecond

	if _, err := r.Run(context.Background(), `while (true) {}`); err == nil {
		t.Fatal("expected the runaway script to be trapped, got nil error")
	}
}

// The one gate end to end, through the REAL interpreter: nocturn.call(tool, args)
// reaches the named tool's Call with its args (as the interpreter JSON-stringifies
// them), and the result flows back to the script — the SAME shared toolset the
// model dispatches through.
func TestRunner_Dispatch_RoutesThroughSharedToolset(t *testing.T) {
	requireInterpreter(t)
	var gotArgs string
	r := script.New(toolset(t, recordingTool(t, "echo", "PONG", &gotArgs)))

	out, err := r.Run(context.Background(), `console.log(nocturn.call("echo", {v: 42}))`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(out) != "PONG" {
		t.Fatalf("stdout = %q, want %q", out, "PONG")
	}
	if gotArgs != `{"v":42}` {
		t.Fatalf("tool received args %q, want %q", gotArgs, `{"v":42}`)
	}
}

// An argless call (whose args field the interpreter drops as undefined) reaches the
// tool as an empty JSON object, not an empty string — the dispatcher defaults it.
func TestRunner_Dispatch_EmptyArgsDefaultsToObject(t *testing.T) {
	requireInterpreter(t)
	var gotArgs string
	r := script.New(toolset(t, recordingTool(t, "echo", "ok", &gotArgs)))

	// Passing undefined makes JSON.stringify drop the args property entirely, so the
	// host sees no args and must default to "{}".
	if _, err := r.Run(context.Background(), `nocturn.call("echo", undefined)`); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotArgs != "{}" {
		t.Fatalf("tool received args %q, want %q", gotArgs, "{}")
	}
}

// The interpreter never dispatches itself: a script calling nocturn.call("code_run", …)
// is refused (no recursive interpreter), surfaced to the guest as a catchable error.
func TestRunner_Dispatch_RefusesCodeRunReentry(t *testing.T) {
	requireInterpreter(t)
	r := script.New(nil)

	out, err := r.Run(context.Background(),
		`try { nocturn.call("code_run", {source: "1"}); console.log("REACHED"); } catch (e) { console.log(e.message); }`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(out, "REACHED") || !strings.Contains(out, "code_run is not callable") {
		t.Fatalf("stdout = %q, want code_run rejected from within a script", out)
	}
}

// An unknown tool is fail-closed: the dispatcher returns an error, surfaced to the
// guest as a catchable JS exception — the host never crashes.
func TestRunner_Dispatch_UnknownTool_SurfacesErrorToGuest(t *testing.T) {
	requireInterpreter(t)
	r := script.New(nil)

	out, err := r.Run(context.Background(),
		`try { nocturn.call("nope", {}); console.log("REACHED"); } catch (e) { console.log("caught: " + e.message); }`)
	if err != nil {
		t.Fatalf("Run should not fail on a guest-visible tool error: %v", err)
	}
	if strings.Contains(out, "REACHED") || !strings.Contains(out, "nope") {
		t.Fatalf("stdout = %q, want a caught error naming the unknown tool", out)
	}
}

// Tool exposes the runner as code_run: it parses its {"source": "..."} argument,
// runs that source, and returns the script's stdout; a missing or malformed argument
// is rejected rather than running an empty script.
func TestRunner_Tool_ParsesSourceArg(t *testing.T) {
	requireInterpreter(t)
	var gotArgs string
	r := script.New(toolset(t, recordingTool(t, "echo", "OK", &gotArgs)))

	tl, err := r.Tool()
	if err != nil {
		t.Fatalf("Tool: %v", err)
	}
	if tl.Spec().Name != "code_run" {
		t.Fatalf("tool name = %q, want code_run", tl.Spec().Name)
	}

	args, _ := json.Marshal(map[string]string{"source": `console.log(nocturn.call("echo", {a: 1}))`})
	out, err := tl.Call(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if strings.TrimSpace(out) != "OK" {
		t.Fatalf("out = %q, want OK", out)
	}
	if gotArgs != `{"a":1}` {
		t.Fatalf("tool received args %q, want %q", gotArgs, `{"a":1}`)
	}

	// Bad arguments are reported, not run.
	if _, err := tl.Call(context.Background(), `{}`); err == nil {
		t.Fatal("expected an error for missing source, got nil")
	}
	if _, err := tl.Call(context.Background(), `not json`); err == nil {
		t.Fatal("expected an error for invalid arguments, got nil")
	}
}
