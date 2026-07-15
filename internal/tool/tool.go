// Package tool is the tool bus: the model-facing tool abstraction (Tool/Spec)
// plus the one Registry every dispatcher shares — the agentic loop, the script
// interpreter, and plugins. It is a NEUTRAL contract between effect PROVIDERS
// (netcap, script, plugin) and CONSUMERS (brain's loop, the TUI observer), so a
// provider never imports the high-level loop. It depends on nothing inside the
// project (stdlib only), so it can never form an import cycle.
package tool

import (
	"context"
	"encoding/json"
)

// Spec is the declaration a Model sees: the tool's name, a description, and a
// JSON Schema for its arguments.
type Spec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	// MaxResult overrides the caller's default per-result byte budget when > 0.
	// Set it only for tools whose output is durable instruction text (e.g. a
	// skill body) that must not be truncated like a bounded tool result.
	MaxResult int
}

// Tool is an invocable capability. Invoke receives the raw JSON arguments; it is
// responsible for unmarshalling and validating them (returning an error the caller
// feeds back to the model on bad input).
type Tool struct {
	Spec
	Invoke func(ctx context.Context, args string) (string, error)
}
