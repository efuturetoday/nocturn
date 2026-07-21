package script

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/app/sandbox"
)

const (
	// gateName is the single host import the interpreter declares and the only HostFunc we register.
	// The guest reaches every tool through it.
	gateName = "call"
	// codeRunName is the model tool that runs a script. Underscore, not a dot: strict tool-call
	// providers only accept ^[a-zA-Z0-9_-]{1,64}$.
	codeRunName = "code_run"
)

// Runner evaluates JS source on the QuickJS interpreter, dispatching a script's nocturn.call actions
// through a shared agentkit.ToolSet — the SAME set the model dispatches through, so script actions
// are gated and observed identically. Build it with New; the zero value is not usable.
type Runner struct {
	tools   agentkit.ToolSet // shared dispatch set (also the model's)
	Timeout time.Duration    // per-run wall-clock bound (0 = sandbox default)
}

// Run evaluates source on the interpreter and returns what the script printed to stdout. The runtime
// prelude (fetch/fs/btoa/…) is always prepended — a Runner only ever drives the JS interpreter. A
// trap, non-zero exit, memory exhaustion, or timeout returns an error alongside any stderr the guest
// produced. Output is not truncated here — the caller bounds what the model sees; a script's own
// action results stay whole.
func (r *Runner) Run(ctx context.Context, source string) (string, error) {
	eng, err := interpreterEngine()
	if err != nil {
		return "", err
	}
	gate := sandbox.HostFunc{Name: gateName, Fn: r.dispatch}
	res, err := eng.Run(ctx, sandbox.Config{
		Stdin:   []byte(withPrelude(source)),
		Hosts:   []sandbox.HostFunc{gate},
		Timeout: r.Timeout,
	})
	if err != nil {
		if len(res.Stderr) > 0 {
			return "", fmt.Errorf("%w: %s", err, res.Stderr)
		}
		return "", err
	}
	return string(res.Stdout), nil
}

// dispatch is the one gate. The guest calls nocturn.call(reqPtr,reqLen) with a request
// {"tool":"http_get","args":{...}}; dispatch runs that tool's Call — the same gated path the model
// takes. An unknown tool or a Call error is returned as an error, which the sandbox surfaces to the
// guest as an "error: ..." string (the guest binding turns it into a JS exception), so a denied
// action never crashes the host.
func (r *Runner) dispatch(ctx context.Context, req []byte) ([]byte, error) {
	var call struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(req, &call); err != nil {
		return nil, fmt.Errorf("bad gate request: %w", err)
	}
	// The interpreter does not dispatch itself: code_run is reachable by the model but never
	// re-entrant from within a script (no recursive interpreter).
	if call.Tool == codeRunName {
		return nil, fmt.Errorf("%s is not callable from within a script", codeRunName)
	}
	args := string(call.Args)
	if args == "" {
		args = "{}"
	}
	// The shared toolset runs the tool's Call — the same gated path the model takes.
	out, err := r.tools.Call(ctx, call.Tool, args)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

const codeRunDescription = "Run a JavaScript program and return what it prints to stdout (console.log/print). " +
	"Use it for multi-step computation and data shaping. " +
	"Inside the script, perform an action by calling nocturn.call(toolName, args): it accepts the " +
	"SAME tool names and argument schemas as the other tools available to you (pass the tool's name " +
	"and the same arguments object you would pass that tool). It is SYNCHRONOUS and returns the " +
	"result directly (a string) — no await needed, though top-level await also works. Such calls go " +
	"through the same authorization and may require approval; code_run itself is not callable from within a script."

// Tool exposes the runner to the model as code_run. Pure compute needs no approval; actions the
// script performs are gated by the tools it calls through the gate.
func (r *Runner) Tool() (agentkit.Tool, error) {
	return agentkit.NewTool(codeRunName, codeRunDescription,
		func(ctx context.Context, args string) (string, error) {
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
		agentkit.WithSchema(agentkit.Object(
			agentkit.Prop("source", agentkit.String("JavaScript source to evaluate")),
		).Require("source")),
	)
}
