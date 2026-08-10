package workspace

import (
	"fmt"
	"maps"
	"slices"

	"log/slog"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/runtime"
	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/plugin"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/skill"
)

// snapshot is everything workspace discovery derives — the agents, skills, plugins and MCP servers
// found on disk, the toolset they add up to, and the per-agent runtimes over it. It is built whole,
// published whole, and never mutated afterwards.
//
// The cut it makes with the rest of Workspace is not "what survives a reload" but the question that
// decides that: WHAT MAY NOT EXIST TWICE. There can only ever be one vault handle on vault.enc, one
// reminder timer per reminder, one chat store on chats/, one knowledge index — so those stay durable
// and are never rebuilt. Two toolsets, two sets of MCP connections are harmless side by side. That is
// exactly why the derived half can be replaced while the previous one is still in use, and why
// reopening the whole workspace cannot: closing first leaves a window with no workspace at all (MCP
// handshakes take seconds), and opening first would put two vaults and two reminder sets on the same
// files.
//
// Nothing here is bound at session start. The root runtime is durable and asks for the CURRENT
// snapshot once per turn, so a skill or an MCP server installed mid-conversation is there in the very
// next message rather than in the next conversation. A turn still works with one fixed set
// throughout — the model is handed a tool list and plans against it, so a tool must not vanish
// between two calls it already decided to make together.
type snapshot struct {
	tools   agentkit.ToolSet
	skills  agentkit.SkillSet
	agents  agent.Set
	persona string

	// One runtime per declared agent, by name — and these DO stay with the run that started under
	// them. An agent's cage is its authority boundary, and widening it mid-run, with nobody watching,
	// is the one place where "pick the new tools up immediately" would be the wrong answer. It is the
	// same rule that makes Autonomy a declaration property: what a run may do is fixed when it fires.
	agentRuntimes map[string]*runtime.Runtime

	plugins []*plugin.Plugin
	mcp     []MCPStatus
	names   inventoryNames
}

// pass is the shared context of ONE discovery run: where to look, what to bind credentials on, what
// to scan with, and where to report what it skipped.
//
// It exists because every step of a run needs the same five things, and threading them as parameters
// produced two signatures that no longer fit on a line and carried six arguments each. Grouping them
// also says something true: they belong to the run, not to the step — a step that took a different
// dir or a different injector would not be part of the same pass.
type pass struct {
	dir      string
	injector *secret.Injector
	scanner  *secret.Scanner
	diag     *agentkit.Diagnostics
	log      *slog.Logger
}

// inventoryNames is what Inventory reports about discovery, carried with the snapshot it describes so
// the two can never be read out of step.
type inventoryNames struct {
	plugins []string
	skills  []string
}

// snapshot returns the current derived state. It is read lock-free: a reload publishes a whole new
// snapshot with one atomic store, so a reader either sees all of the old one or all of the new.
func (w *Workspace) snapshot() *snapshot { return w.cur.Load() }

// Reload re-runs discovery and publishes the result.
//
// New sessions get the new toolset, skills and runtimes; sessions already running keep the ones they
// opened with and finish on them (chat.Manager resolves a runtime when a session opens, not when the
// manager is built). Nothing durable is touched — the chat stores, reminders, wakes, vault, voice
// sessions and the knowledge index all carry straight through.
//
// It is single-flight: two devices installing something at once queue rather than interleave, and a
// failed discovery pass leaves the previous one standing untouched, so there is no half-swapped workspace
// to be in.
func (w *Workspace) Reload() error {
	w.reloadMu.Lock()
	defer w.reloadMu.Unlock()

	next, err := w.discover()
	if err != nil {
		return err
	}
	old := w.cur.Swap(next)
	if old == nil {
		return nil // the first snapshot, published by Open: nothing to retire, nothing to restart
	}
	w.retire(old)
	w.restartScheduler()
	return nil
}

// retire parks a superseded snapshot's plugins for Close to release. See Workspace.retired for why
// they are not closed here.
func (w *Workspace) retire(old *snapshot) {
	if len(old.plugins) == 0 {
		return
	}
	w.retiredMu.Lock()
	w.retired = append(w.retired, old.plugins...)
	w.retiredMu.Unlock()
}

// assemble runs discovery and builds everything derived from it. It reads the workspace's durable
// parts and mutates nothing on w — the caller publishes the result.
func (w *Workspace) discover() (*snapshot, error) {
	// One diagnostics collector drains every kind's discovery — agents, skills, plugins, MCP all feed
	// their skipped/shadowed items here, and it is logged once below. A malformed item is skipped
	// (fail-closed: its authority is simply absent), never fatal.
	var diag agentkit.Diagnostics
	p := pass{dir: w.dir, injector: w.sec.injector, scanner: w.sec.scanner, diag: &diag, log: w.log}

	agents := agent.Discover(w.path("agents"), &diag)

	// Skills: load the workspace's skills/ folder into an agentkit.SkillSet. agentkit surfaces the
	// catalog (system prompt) and skill_load per top-level session; skill_read (for a skill's bundled
	// files) joins the base tools, so it flows into the cages like the file tools. An invalid skill is
	// skipped inside Load with a logged warning — never blocks the workspace.
	//
	// This is the only base tool discovery decides, which is why baseTools is durable and only this is
	// appended: everything else in it belongs to an object that must not exist twice.
	skills, skillDirs := skill.Discover(w.path("skills"), &diag)
	baseTools := slices.Clone(w.baseTools)
	if len(skillDirs) > 0 {
		readTool, err := skill.ReadTool(skillDirs)
		if err != nil {
			return nil, fmt.Errorf("workspace %q: skill_read: %w", w.name, err)
		}
		baseTools = append(baseTools, readTool)
	}
	base, err := agentkit.NewToolSet(baseTools...)
	if err != nil {
		return nil, fmt.Errorf("workspace %q: toolset: %w", w.name, err)
	}

	// A fresh map every time. installPlugins and installMCP below fold their tools in by writing to
	// it, so handing on the previous pass's set would have two snapshots writing one map.
	toolset, err := buildTools(base, w.llm, agents, w.mem)
	if err != nil {
		return nil, fmt.Errorf("workspace %q: toolset: %w", w.name, err)
	}

	// Discover + install plugins as top-level tools, each caged to a subset of the base tools and
	// gated exactly like the model's own calls. Their credentials are bound host-side on the injector,
	// reconciled against the previous snapshot's so a plugin removed from disk stops injecting.
	var prevPlugins []*plugin.Plugin
	if prev := w.snapshot(); prev != nil {
		prevPlugins = prev.plugins
	}
	plugins, err := p.installPlugins(base, toolset, prevPlugins)
	if err != nil {
		return nil, fmt.Errorf("workspace %q: plugins: %w", w.name, err)
	}

	// The credential wiring that DEPENDS ON DISCOVERY is re-run here, and it belongs here for a reason
	// that only shows up on the second pass: a server added while the daemon runs has a shard nobody
	// has read and an OAuth source nobody has registered. Leaving these in the durable half is what
	// made "add an MCP server from the phone, authorize it, use it" impossible without a restart —
	// the token was stored and the injector had no resolver that could find it.
	//
	// Both are idempotent by construction: LoadShardsInto copies by name and SetResolver replaces by
	// name. Plugin bindings are not (AddBinding appends), which is why installPlugins clears each
	// owner's first.
	w.sec.reconcile(w.dir, w.name, w.log)

	// Discover + connect the remote MCP servers declared in <dir>/mcp/*.json and fold their tools in
	// (as <server>_<tool>), each gated on the net host-allowlist like http_read/http_write (ADR-9).
	mcpStatus := p.installMCP(toolset)

	// The persona is resolved per discovery pass, not once per process. The rule it answers to is that the
	// assistant's identity must not shift MID-TURN, and the turn boundary already guarantees that: the
	// root runtime reads this snapshot when a turn starts and works with it throughout. Resolving it
	// here is what makes editing PERSONA.md take effect on the next turn instead of the next restart.
	persona := resolvePersona(w.dir, w.log)

	// One runtime per declared agent (its cage + gate + autonomy). Autonomy is a declaration property,
	// so a run's authority never depends on when it fires. The agent manager's resolver maps a run to
	// its owner's runtime via the persisted Meta.Agent; a run whose agent was deleted resolves to
	// readOnly (no tools) so its transcript still opens but it cannot act.
	agentRuntimes := make(map[string]*runtime.Runtime, len(agents.All()))
	for _, a := range agents.All() {
		art, err := w.agentRuntime(base, a)
		if err != nil {
			return nil, fmt.Errorf("workspace %q: %w", w.name, err)
		}
		agentRuntimes[a.Name] = art
	}

	names := inventoryNames{plugins: pluginNames(plugins), skills: slices.Sorted(maps.Keys(skillDirs))}

	// Every kind's discovery skips (bad agent/skill/plugin/server) drained through the one collector,
	// logged uniformly here — a single place an operator scans for what did NOT load and why.
	for _, d := range diag.All() {
		w.log.With("component", "discovery").Warn("skipped", "subject", d.Subject, "detail", d.Message)
	}
	// One readiness line stating what was discovered — so an operator sees the assembled stack at a
	// glance instead of inferring it from behavior. Logged on every pass, because a reload is
	// exactly the moment somebody wants to know what the workspace can do now.
	w.log.With("component", "workspace").Info("workspace assembled",
		"agents", len(agents.All()), "skills", len(names.skills), "plugins", len(names.plugins),
		"mcp", len(mcpStatus), "tools", len(toolset), "skipped", diag.Len())

	return &snapshot{
		tools:         toolset,
		skills:        skills,
		agents:        agents,
		persona:       persona,
		agentRuntimes: agentRuntimes,
		plugins:       plugins,
		mcp:           mcpStatus,
		names:         names,
	}, nil
}
