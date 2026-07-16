package script_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/script"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// recordingTool is a fake tool.Tool that captures the args it was invoked with
// and returns a fixed output — enough to prove the gate dispatches correctly.
func recordingTool(name, out string, gotArgs *string) tool.Tool {
	return tool.Tool{
		Spec: tool.Spec{Name: name},
		Invoke: func(_ context.Context, args string) (string, error) {
			if gotArgs != nil {
				*gotArgs = args
			}
			return out, nil
		},
	}
}

// A code.run script has NO ambient filesystem: the runner passes no Workspace to
// the sandbox (proven at the sandbox layer by TestRun_NoWorkspace_HasNoFilesystem),
// so the guest has no WASI preopen and the ONLY route to a file is a brokered
// file.* tool through the gate. With no file tool registered, a script's fs access
// therefore FAILS — there is no un-brokered WASI fallback. If a mount ever leaked
// in, the fs shim would have a bypass and this would stop erroring.
func TestRun_NoAmbientFilesystem(t *testing.T) {
	// Registry deliberately has NO file.read — so the gate has nothing to dispatch.
	r := script.New(tool.NewRegistry(nil))

	_, err := r.Run(context.Background(), `
		const fs = require("fs");
		fs.readFileSync("/etc/hostname"); // no gated tool → must throw, no WASI fallback
		console.log("REACHED");
	`)
	if err == nil {
		t.Fatal("script reached the filesystem with no gated file tool — an un-brokered FS path exists")
	}
	// And it fails because the tool is absent, not because the file is missing.
	if !strings.Contains(err.Error(), "file.read") && !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("err = %v, want an unknown-tool (no file.read) failure", err)
	}
}

// The one gate end to end, through the REAL interpreter: a script's
// nocturn.call(tool, args) reaches the named tool's Invoke with its args (as the
// interpreter JSON-stringifies them), and the result flows back to the script.
func TestRun_GateDispatchesToTool(t *testing.T) {
	var gotArgs string
	r := script.New(tool.NewRegistry([]tool.Tool{recordingTool("echo", "PONG", &gotArgs)}))

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

// An unknown tool is fail-closed: the dispatcher returns an error, which the
// sandbox surfaces to the guest as an "error: ..." string that the interpreter's
// binding turns into a catchable JS exception — the host never crashes.
func TestRun_UnknownTool_SurfacesErrorToGuest(t *testing.T) {
	r := script.New(nil)

	out, err := r.Run(context.Background(), `try { nocturn.call("nope", {}); console.log("REACHED"); } catch (e) { console.log("caught: " + e.message); }`)
	if err != nil {
		t.Fatalf("Run should not fail on a guest-visible tool error: %v", err)
	}
	if strings.Contains(out, "REACHED") || !strings.Contains(out, "unknown tool nope") {
		t.Fatalf("stdout = %q, want a caught error naming the unknown tool", out)
	}
}

// A tool's own Invoke error reaches the guest the same way (e.g. a denied
// effect: Guard.Authorize → ErrDenied → thrown JS exception).
func TestRun_ToolError_SurfacesErrorToGuest(t *testing.T) {
	boom := tool.Tool{
		Spec:   tool.Spec{Name: "boom"},
		Invoke: func(context.Context, string) (string, error) { return "", errors.New("kaboom") },
	}
	r := script.New(tool.NewRegistry([]tool.Tool{boom}))

	out, err := r.Run(context.Background(), `try { nocturn.call("boom", {}); } catch (e) { console.log(e.message); }`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "kaboom") {
		t.Fatalf("stdout = %q, want it to contain the tool error", out)
	}
}

// The interpreter never dispatches itself: a script calling nocturn.call("code.run", …)
// is refused (no recursive interpreter), surfaced to the guest as a catchable error.
func TestRun_CodeRunNotReentrant(t *testing.T) {
	r := script.New(tool.NewRegistry(nil))

	out, err := r.Run(context.Background(), `try { nocturn.call("code.run", {source: "1"}); } catch (e) { console.log(e.message); }`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "code.run is not callable") {
		t.Fatalf("stdout = %q, want code.run rejected", out)
	}
}

// code.run is the model-facing tool: it takes {"source": "..."} and runs it,
// returning the script's stdout.
func TestTool_CodeRun_RunsSource(t *testing.T) {
	var gotArgs string
	r := script.New(tool.NewRegistry([]tool.Tool{recordingTool("echo", "OK", &gotArgs)}))

	tl := r.Tool()
	if tl.Name != "code.run" {
		t.Fatalf("tool name = %q, want code.run", tl.Name)
	}
	args, _ := json.Marshal(map[string]string{"source": `console.log(nocturn.call("echo", {a: 1}))`})
	out, err := tl.Invoke(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if strings.TrimSpace(out) != "OK" {
		t.Fatalf("out = %q, want OK", out)
	}
	if gotArgs != `{"a":1}` {
		t.Fatalf("tool received args %q", gotArgs)
	}
}

// code.run validates its own arguments (structure + our validation), reporting bad
// input rather than running an empty script.
func TestTool_CodeRun_RejectsMissingSource(t *testing.T) {
	r := script.New(nil)
	tl := r.Tool()

	if _, err := tl.Invoke(context.Background(), `{}`); err == nil {
		t.Fatal("expected an error for missing source, got nil")
	}
	if _, err := tl.Invoke(context.Background(), `not json`); err == nil {
		t.Fatal("expected an error for invalid arguments, got nil")
	}
}
