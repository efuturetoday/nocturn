// Package script runs untrusted JavaScript on the sandbox interpreter (QuickJS
// compiled to wasm) and bridges effects through a single generic gate.
//
// The interpreter guest declares exactly ONE host import — nocturn.call — and
// the host side of that import is a dispatcher over a tool.Tool registry: the
// guest calls nocturn.call(tool, args), the dispatcher looks the tool up and
// runs its Invoke. That registry is the SAME set of gated tools the model uses
// (netcap.Net.Tools()), so a script reaches effects through the identical
// authorization pipeline (Guard.Authorize + out-of-band HITL). One gate is the
// reference monitor; adding a capability is a Go-side change and never rebuilds
// the interpreter.
//
// Pure compute needs no approval (ADR-1): a script that never calls the gate
// performs no effect. Each effect it does perform is gated individually by the
// tool it names.
package script

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/efuturetoday/nocturn/internal/sandbox"
	"github.com/efuturetoday/nocturn/internal/tool"
)

const (
	// gateName is the single host import the interpreter declares and the only
	// HostFunc we register. The guest reaches every capability through it.
	gateName = "call"
	// codeRunName is the brain tool that runs a script.
	codeRunName = "code.run"
)

// Runner evaluates JS source on the QuickJS interpreter, dispatching a script's
// nocturn.call effects through a shared tool.Registry — the SAME registry the
// model dispatches through, so script effects are gated and observed identically.
// Build it with New; the zero value is not usable.
type Runner struct {
	Guest    []byte         // the QuickJS interpreter wasm
	Registry *tool.Registry // shared dispatch registry (also the model's)
	Timeout  time.Duration  // per-run wall-clock bound (0 = sandbox default)
	MaxPages uint32         // memory cap in 64 KiB pages (0 = sandbox default)
}

// Run evaluates source on the interpreter and returns what the script printed to
// stdout. The runtime prelude (fetch/fs/btoa/…) is always prepended — a Runner
// only ever drives the JS interpreter. A trap, non-zero exit, memory exhaustion,
// or timeout returns an error alongside any stderr the guest produced. Output is
// not truncated here — the brain bounds what the model sees; a script's own effect
// results stay whole.
func (r *Runner) Run(ctx context.Context, source string) (string, error) {
	gate := sandbox.HostFunc{Name: gateName, Fn: r.dispatch}
	res, err := sandbox.Run(ctx, r.Guest, sandbox.Config{
		Stdin:    []byte(withPrelude(source)),
		Hosts:    []sandbox.HostFunc{gate},
		Timeout:  r.Timeout,
		MaxPages: r.MaxPages,
	})
	if err != nil {
		if len(res.Stderr) > 0 {
			return "", fmt.Errorf("%w: %s", err, res.Stderr)
		}
		return "", err
	}
	return string(res.Stdout), nil
}

// dispatch is the one gate. The guest calls nocturn.call(reqPtr,reqLen) with a
// request {"tool":"http.read","args":{...}}; dispatch runs that tool's Invoke —
// the same gated path the model takes. An unknown tool or an Invoke error is
// returned as an error, which the sandbox surfaces to the guest as an
// "error: ..." string (the guest binding turns it into a JS exception), so a
// denied effect never crashes the host.
func (r *Runner) dispatch(ctx context.Context, req []byte) ([]byte, error) {
	var call struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(req, &call); err != nil {
		return nil, fmt.Errorf("bad gate request: %w", err)
	}
	// The interpreter does not dispatch itself: code.run is reachable by the model
	// but never re-entrant from within a script (no recursive interpreter).
	if call.Tool == codeRunName {
		return nil, fmt.Errorf("%s is not callable from within a script", codeRunName)
	}
	args := string(call.Args)
	if args == "" {
		args = "{}"
	}
	// The shared Registry runs the tool's Invoke — the same gated path the model
	// takes — and emits the observer events, so this script call is seen too.
	out, err := r.Registry.Invoke(ctx, call.Tool, args)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// Tool exposes the runner to the brain as code.run. Pure compute needs no
// approval; effects the script performs are gated by the tools it calls through
// the gate. This is the model-facing counterpart to running a script.
func (r *Runner) Tool() tool.Tool {
	return tool.Tool{
		Spec: tool.Spec{
			Name: codeRunName,
			Description: "Run a JavaScript program and return what it prints to stdout (console.log/print). " +
				"Use it for multi-step computation and data shaping. " +
				"Inside the script, perform an effect by calling nocturn.call(toolName, args): it accepts the " +
				"SAME tool names and argument schemas as the other tools available to you (pass the tool's name " +
				"and the same arguments object you would pass that tool). It is SYNCHRONOUS and returns the " +
				"result directly (a string) — no await needed, though top-level await also works. Such calls go " +
				"through the same authorization and may require approval; code.run itself is not callable from within a script.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"source":{"type":"string","description":"JavaScript source to evaluate"}` +
				`},"required":["source"]}`),
		},
		Invoke: func(ctx context.Context, args string) (string, error) {
			var a struct {
				Source string `json:"source"`
			}
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if a.Source == "" {
				return "", errors.New("missing required field: source")
			}
			return r.Run(ctx, a.Source)
		},
	}
}
