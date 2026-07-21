// Package tools bundles nocturn's own gated tools — the thin ones whose whole body is "build an
// agentkit.Tool that gates its target on a shared axis before it acts". They share only the gate
// model (each owns its own axis constant and target matcher), so they live together here rather than
// one sprawling package per tool. Heavier capabilities that carry their own runtime — code.run
// (QuickJS/wasm) and plugins — stay in their own packages; they just contribute agentkit.Tools that
// Base folds in the same way.
package tools

import (
	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/app/script"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// Base builds nocturn's base tools — the set every chat and agent draws from before a per-agent cage
// narrows it. It grows as capabilities land (file, notify, time, …). Returned as a slice so the
// caller can both form the base ToolSet and scope per-agent subsets from it. code_run is NOT here: it
// is woven per cage by Compose, so a script's reach is bounded to exactly the tools of the cage it
// runs in. creds and scanner (either may be nil) are the host-owned credential jar the network tool
// injects from and the bidirectional leak scanner it screens traffic through.
func Base(creds *secret.Injector, scanner *secret.Scanner) ([]agentkit.Tool, error) {
	httpTool, err := New(creds, scanner).Tool()
	if err != nil {
		return nil, err
	}
	return []agentkit.Tool{httpTool}, nil
}

// Compose finalizes one cage: the tools of `cage`, plus — only when allowCodeRun — a code_run whose
// script dispatches over EXACTLY those same tools and nothing more. This is the security seam for
// code_run: it can never widen authority beyond its cage. An agent caged to a subset that includes
// code_run reaches only that subset from a script too; an agent NOT granted code_run gets no
// interpreter at all. (code_run never dispatches itself — reentry is refused — nor sub-agent tools,
// which are layered on outside a cage.)
func Compose(cage agentkit.ToolSet, allowCodeRun bool) (agentkit.ToolSet, error) {
	if !allowCodeRun {
		return cage, nil
	}
	// script.New captures `cage` (without code_run) as its dispatch set — the reach bound.
	codeRun, err := script.New(cage).Tool()
	if err != nil {
		return nil, err
	}
	members := make([]agentkit.Tool, 0, len(cage)+1)
	for _, t := range cage {
		members = append(members, t)
	}
	members = append(members, codeRun)
	return agentkit.NewToolSet(members...)
}

// CodeRunTool is the tool name Compose adds, so callers can test an agent's filter for it.
const CodeRunTool = "code_run"
