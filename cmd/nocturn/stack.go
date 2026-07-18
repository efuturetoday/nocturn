package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/approval"
	"github.com/efuturetoday/nocturn/internal/appserver"
	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/notifycap"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/wakecap"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// shared is what EVERY workspace stack shares — the process-wide spine. Nothing here
// is workspace-specific: one master key (derives each vault's key), one HITL engine
// (workspace-agnostic; routes by autonomy, not workspace), one stateless LLM client,
// one clock tool, and one sink to the single bubbletea program. Passed to buildStack.
type shared struct {
	master    *secret.Master
	approvals *hitl.Engine
	llmModel  brain.Model      // stateless client, safe to share across stacks
	notify    notifycap.Pusher // out-of-band push (ntfy) or the attended TUI fallback
	send      func(tea.Msg)    // p.Send, late-bound (p is created after the stacks)
	modelName string
	sync      *syncHub // process-wide client-sync fan-out (badges + chat-list changes); nil = no app server
}

// bound is one OPEN workspace: the workspace itself (which owns tools/skills/agents/guard/
// grants) plus its multi-chat manager, self-waker, and scheduler. Both the TUI and the app
// drive the SAME manager chats — there is no privileged per-front-end loop. The chatModel
// rebinds to another workspace on /ws switch. Isolation is structural: each workspace's guard
// and injector hold only its own secrets/grants, so N run in one process without crossing.
type bound struct {
	ws        *workspace.Workspace
	chats     *chat.Manager // the workspace's live chats (N persistent named), driven by TUI + app alike
	scheduler *agent.Scheduler
}

// basePolicy is the workspace base on the WIRKUNG axis: reads run still (Allow),
// writes ask (Ask) — for every family. Reach is bounded per caller by cages + grants.
func basePolicy() capability.Policy {
	return capability.Policy{Rules: []capability.Rule{
		{Family: capability.Wildcard, TargetGlob: capability.Wildcard, Writes: capability.MatchRead, Effect: capability.Allow, Epoch: capability.Permanent},
		{Family: capability.Wildcard, TargetGlob: capability.Wildcard, Writes: capability.MatchWrite, Effect: capability.Ask, Epoch: capability.Permanent},
	}}
}

// discoverWorkspaces returns the names of every workspace directory under root
// (skipping files like master.json). Sorted. The active workspace (which may not yet
// exist on disk) is unioned in by the caller.
func discoverWorkspaces(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && wsNameRe.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	return names, nil
}

// buildStack assembles one workspace's isolated stack: unlock its vault (via the
// shared master), its own epochs+guard+injector+registry+session, its plugins/MCP/
// skills/agents, and its own scheduler. This is exactly the per-workspace sequence
// that used to live inline in tuiCmd — extracted so N of them can run in one process
// (and, later, a headless daemon can reuse it without a TUI).
func buildStack(ctx context.Context, sh shared, wsName, wsDir string) (*bound, error) {
	// The workspace OWNS the composition (broker, tools, skills, agents, loop, grants).
	// This function keeps only what a domain package must not hold: interactive
	// provisioning (plugins/MCP/OAuth prompts) and the TUI transport (self-wake, scheduler
	// log) — wired below over the workspace's exposed parts. (The activity sink now lives
	// in the chatModel, stamped per turn there.)
	host := workspace.Host{Master: sh.master, Approvals: sh.approvals, Model: sh.llmModel, Notify: sh.notify}
	unlock := func(dir, name string) (*secret.Vault, error) {
		return unlockVault(sh.master, filepath.Join(dir, "secrets.vault"), name)
	}
	w, err := workspace.Open(host, basePolicy, unlock, wsName, wsDir)
	if err != nil {
		return nil, err
	}
	reportSkills(w.Skills())
	reportAgents(w.Agents())

	// Interactive extensions wire against the workspace's own parts. ALL credentials —
	// including OAuth — come from plugin manifests (loadPlugins → wirePluginOAuth); there
	// is no built-in integration wired here.
	approvals := approval.Load(filepath.Join(wsDir, "approved.json"))
	if err := loadPlugins(ctx, w.Tools(), w.Credentials(), w.Vault(), approvals, wsDir); err != nil {
		return nil, err
	}
	if err := loadMCP(ctx, w.Tools(), w.Guard(), w.Credentials(), w.LeakScanner(), w.Vault(), approvals, wsDir); err != nil {
		return nil, err
	}

	// An in-chat /agent spawn resolves its charter here — ATTENDED: a human is at the
	// parent chat, so an Ask goes through normal HITL (the agent's autonomy dial is
	// only for unattended scheduled firings).
	agentCharter := func(name string) (chat.Charter, error) {
		return w.AgentCharter(name, capability.AutonomyAttended)
	}

	// The chat manager owns this workspace's live chats — one serialized loop per chat, each
	// seeded from its saved history and kept alive on the process ctx. BOTH the TUI and the app
	// open chats from here; there is no separate per-front-end loop. Each chat binds wake to
	// itself (its ctx decorator), so a wake resumes the chat that scheduled it.
	chatStore := chat.LoadStore(filepath.Join(wsDir, "chats"))
	// onActivity badges this workspace's background chats; onChange pushes the full chat list
	// on any list change. Both nil under the TUI (no hubs), so the manager stays silent there.
	var onActivity func(chatID, kind string)
	var onChange func()
	if sh.sync != nil {
		onActivity = func(chatID, kind string) { sh.sync.emitActivity(wsName, chatID, kind) }
		onChange = func() { sh.sync.emitList(appserver.DomainChats, wsName) }
		// reminders live-sync: a create/fire/cancel pushes the full reminder list to clients.
		w.Reminders().OnChange = func() { sh.sync.emitList(appserver.DomainReminders, wsName) }
	}
	chatMgr := chat.NewManager(ctx, chat.Deps{
		Engine:     w.Brain(),
		Guard:      w.Guard(),
		Store:      chatStore,
		Root:       w.RootCharter,
		Agent:      agentCharter,
		OnActivity: onActivity,
		OnChange:   onChange,
	})

	// wake: a running turn schedules its OWN resume after a delay (self-paced loops / polling);
	// ungated, bounded (delay clamp + pending cap). One workspace-shared Waker serves every
	// chat — each wake reads its resume from the calling turn's ctx (set by each chat's
	// decorator in the manager), so it resumes the chat that invoked it, even in the
	// background with no client attached. The Waker is reachable via the tool it registers
	// (the tool closure captures it), so it needs no field on bound.
	w.Tools().Add(wakecap.New().Tool())

	sched, err := agent.NewScheduler(w.Agents(), func(runCtx context.Context, def agent.Agent) error {
		// A cron firing runs UNATTENDED as a fresh one-shot chat in the manager: its
		// charter carries the agent's declared autonomy dial (no human at this trigger)
		// and its "<ws>/<agent>" provenance label, and the persisted record is the run's
		// audit trail (Origin agent, visible in the picker). No activity sink is stamped
		// → silent by construction. FireAgent owns overlap (ErrAgentBusy while this agent is
		// still running) and autonomy (via the charter) — the scheduler only picks the moment.
		ch, err := w.AgentCharter(def.Name, def.Autonomy)
		if err != nil {
			return err
		}
		return chatMgr.FireAgent(runCtx, def.Name, ch)
	}, agent.WithLog(func(line string) { sh.send(schedulerMsg("[" + wsName + "] " + line)) }))
	if err != nil {
		return nil, err
	}

	return &bound{ws: w, chats: chatMgr, scheduler: sched}, nil
}
