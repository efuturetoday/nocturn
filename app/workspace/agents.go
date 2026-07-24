package workspace

import (
	"context"
	"fmt"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/agentkit/runtime"
	"github.com/efuturetoday/nocturn/app/agent"
	"github.com/efuturetoday/nocturn/app/chat"
)

// sessionRouter resolves a chat id to the manager that owns it, so a fired wake re-opens a run in the
// right store: an agent run (Meta.Agent set) reopens in the agent manager (its own runtime + store),
// any other id in the user manager. It satisfies tools.Sessions (Open by id).
type sessionRouter struct {
	user, agent *chat.Manager
	agentStore  *chat.Store
}

func (r sessionRouter) Open(id string) *agentkit.Session {
	if r.agentStore.OwnerOf(id) != "" {
		return r.agent.Open(id)
	}
	return r.user.Open(id)
}

// Close stops both chat managers (their reapers and every live session). Call once on shutdown.
func (w *Workspace) Close() {
	w.chats.CloseAll()
	w.agentChats.CloseAll()
}

// maxAgentRuns caps how many past runs each agent keeps. Runs are an audit tail (see them in the app),
// not a permanent archive: a cron agent fires forever, so without a cap the agent store grows without
// bound (seen in the wild at thousands of runs). Applied per agent, on every fire and once at Open.
const maxAgentRuns = 50

// Agents returns the workspace's declared agents, sorted by name.
func (w *Workspace) Agents() []agent.Agent { return w.agents.All() }

// AgentChats returns the manager that owns agent-run sessions (the agent-store counterpart of Chats).
func (w *Workspace) AgentChats() *chat.Manager { return w.agentChats }

// ChatManager selects a chat kind's manager: "agent" → agent runs, anything else → user chats. The
// wire passes the kind on every store-addressed chat.* command (mandatory), so one handler set drives
// both managers without the daemon having to derive the store.
func (w *Workspace) ChatManager(kind string) *chat.Manager {
	if kind == "agent" {
		return w.agentChats
	}
	return w.chats
}

// StartAgents runs the cron scheduler until ctx is cancelled — call it in a goroutine.
func (w *Workspace) StartAgents(ctx context.Context) { w.sched.Start(ctx) }

// AgentRuns lists the persisted agent-run transcripts, most recent first.
func (w *Workspace) AgentRuns() ([]chat.Meta, error) { return w.agentStore.Metas() }

// agentRuntime builds the runtime one declared agent's runs spin under: its tool cage (the workspace
// toolset filtered to the agent's declared tools), its instructions as the system prompt, its effort
// and budget, and its autonomy resolved to an approver — Guarded hands runs the workspace's
// out-of-band approver (an Ask reaches the human), Strict (the default) gets none, so any fresh Ask
// is denied fail-closed. Autonomy is a declaration property, so this runtime is static per agent and
// is built once at Open. It bakes no store: the agent manager adds the per-run store on Session.
func (w *Workspace) agentRuntime(a agent.Agent) *runtime.Runtime {
	var appr gate.Approver
	if a.Autonomy == agent.Guarded {
		appr = w.approver // itself nil when no device is wired, which collapses guarded to strict
	}
	budget := turnTimeout
	if a.Budget > 0 {
		budget = a.Budget
	}
	return runtime.New(w.llm,
		runtime.WithTools(w.tools.Select(a.Matches)),
		runtime.WithGate(policy(), w.grants, appr),
		runtime.WithGateLogger(agentkit.SlogLogger(w.log)),
		runtime.WithSession(
			agentkit.WithSystem(a.Instructions),
			agentkit.WithEffort(a.Effort),
			agentkit.WithTimeout(budget),
			agentkit.WithLogger(agentkit.SlogLogger(w.log)),
		),
	)
}

// FireAgent starts the named agent's run over task and returns the run's id. The run is a first-class
// chat in the agent manager: it streams live, persists its transcript to the agent store, and is
// openable by its id — a notify or reminder it sets carries provenance back to it. It is
// fire-and-forget (the run outlives the caller on the manager's background ctx); ctx is only used to
// reject a call once the daemon is shutting down. The run's authority comes from its agentRuntime
// (cage + gate + autonomy-resolved approver).
func (w *Workspace) FireAgent(ctx context.Context, name, task string) (string, error) {
	if _, ok := w.agents.Get(name); !ok {
		return "", fmt.Errorf("workspace %q: no agent %q", w.name, name)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	id := chat.NewID()
	w.log.With("component", "agent").Info("firing agent", "agent", name, "run", id)
	w.agentChats.Fire(id, name, task)
	// Bound this agent's run history: the fresh run (just stamped, newest) is kept; older ones past the
	// cap are dropped. Best-effort — a prune failure must not fail the fire.
	if n, err := w.agentStore.PruneAgent(name, maxAgentRuns); err != nil {
		w.log.Warn("pruning agent runs failed", "agent", name, "err", err)
	} else if n > 0 {
		w.log.Info("pruned old agent runs", "agent", name, "deleted", n, "keep", maxAgentRuns)
	}
	return id, nil
}

// pruneAgentRuns caps every agent's run history to maxAgentRuns at Open — including runs whose agent
// was since deleted (their files would otherwise linger forever). It bounds an existing backlog once,
// then FireAgent keeps each agent bounded from there.
func (w *Workspace) pruneAgentRuns() {
	metas, err := w.agentStore.Metas()
	if err != nil {
		w.log.Warn("listing agent runs to prune failed", "err", err)
		return
	}
	owners := map[string]struct{}{}
	for _, m := range metas {
		if m.Agent != "" {
			owners[m.Agent] = struct{}{}
		}
	}
	total := 0
	for owner := range owners {
		n, err := w.agentStore.PruneAgent(owner, maxAgentRuns)
		if err != nil {
			w.log.Warn("pruning agent runs failed", "agent", owner, "err", err)
			continue
		}
		total += n
	}
	if total > 0 {
		w.log.Info("pruned agent runs at startup", "deleted", total, "keepPerAgent", maxAgentRuns)
	}
}
