// Package tools bundles nocturn's own gated tools — the thin ones whose whole body is "build an
// agentkit.Tool that gates its target on a shared kind before it acts". They share only the gate
// model (each owns its own kind constant and target matcher), so they live together here rather than
// one sprawling package per tool. Heavier tools that carry their own runtime — code_run
// (QuickJS/wasm) and plugins — stay in their own packages; they just contribute agentkit.Tools that
// Base folds in the same way.
package tools

import (
	"log/slog"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/script"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// Config carries the per-workspace wiring the base tools need. Every field may be zero: a nil Secrets
// or Scanner disables credential injection or leak-scanning, an empty Root drops the file tools.
type Config struct {
	Secrets  *secret.Injector // host-owned credential jar the network tool injects from
	Scanner  *secret.Scanner  // bidirectional secret leak scanner
	Root     string           // workspace root; file tools are confined here (empty = no file tools)
	Notifier Notifier         // out-of-band user notification; nil = no notify tool
	Waker    *Waker           // self-continuation scheduler; nil = no wake tool (caller Binds it to the chat manager)
	Logger   *slog.Logger     // effect-trace (http request / path-escape); nil = silent. gate/scanner log their own events.
}

// Base builds nocturn's base tools — the set every chat and agent draws from before a per-agent cage
// narrows it: the network tools (http_read/http_write/dns_resolve/ping), time_now, and — when a Root
// is given — the workspace-confined file tools. It grows as tools land (notify, …). Returned as a
// slice so the caller can both form the base ToolSet and scope per-agent subsets from it. code_run is
// NOT here: it is woven per cage by Compose, so a script's reach is bounded to the tools of its cage.
func Base(cfg Config) ([]agentkit.Tool, error) {
	// One effect-trace logger for the base tools; default to a no-op so every tool logs unconditionally
	// (no nil checks). gate/scanner log their own security events under component=secret/gate.
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	net := New(cfg.Secrets, cfg.Scanner)
	net.SetLogger(log)
	netTools, err := net.Tools()
	if err != nil {
		return nil, err
	}
	timeNow, err := timeTool()
	if err != nil {
		return nil, err
	}
	out := append(netTools, timeNow)
	if cfg.Root != "" {
		fileTools, err := files{root: cfg.Root, scanner: cfg.Scanner, log: log}.Tools()
		if err != nil {
			return nil, err
		}
		out = append(out, fileTools...)
	}
	if cfg.Notifier != nil {
		notifyTool, err := notify{notifier: cfg.Notifier, scanner: cfg.Scanner}.tool()
		if err != nil {
			return nil, err
		}
		out = append(out, notifyTool)
	}
	if cfg.Waker != nil {
		wakeTool, err := cfg.Waker.Tool()
		if err != nil {
			return nil, err
		}
		out = append(out, wakeTool)
	}
	return out, nil
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
