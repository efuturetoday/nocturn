// Package tools bundles nocturn's own gated tools — the thin ones whose whole body is "build an
// agentkit.Tool that gates its target on a shared axis before it acts". They share only the gate
// model (each owns its own axis constant and target matcher), so they live together here rather than
// one sprawling package per tool. Heavier capabilities that carry their own runtime — code.run
// (QuickJS/wasm) and plugins — stay in their own packages; they just contribute agentkit.Tools that
// Base folds in the same way.
package tools

import "github.com/efuturetoday/nocturn/agentkit"

// Base builds nocturn's base tools — the set every chat and agent draws from before a per-agent cage
// narrows it. It grows as capabilities land (file, notify, time, …). Returned as a slice so the
// caller can both form the base ToolSet and scope per-agent subsets from it.
func Base() ([]agentkit.Tool, error) {
	httpTool, err := New().Tool()
	if err != nil {
		return nil, err
	}
	return []agentkit.Tool{httpTool}, nil
}
