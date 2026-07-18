package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/approval"
	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/notifycap"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/session"
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
	activity  *activityHub // process-wide badge fan-out for background chats (nil = no app server)
}

// bound is the TUI's binding to one OPEN workspace: the workspace itself (which owns
// tools/skills/agents/guard/grants) plus its interactive session, self-waker, and
// scheduler. The chatModel drives these objects DIRECTLY — no closure adapter — and
// rebinds to another on /ws switch. Isolation is structural: each workspace's guard and
// injector hold only its own secrets/grants, so N run in one process without crossing.
type bound struct {
	ws        *workspace.Workspace
	session   *session.Session // the TUI's single interactive session; Close() at shutdown, MarkSkill for /name
	runner    *session.Runner  // the turn loop the TUI drives (Submit in, Subscribe out)
	chats     *chat.Manager    // the app's multi-chat manager (N persistent named chats); TUI doesn't use it
	waker     *wakecap.Waker
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

	// A child-agent run routes through this, shared by the TUI runner and the app's chats.
	agentRun := func(runCtx context.Context, name, task string) (string, error) {
		res, err := w.RunAgent(runCtx, name, task)
		return res.Answer, err
	}

	sess := w.OpenSession()

	// The Runner is the single serialized turn loop for THIS workspace's TUI session: the
	// TUI Submits inputs and Subscribes to its events; a self-wake and a scheduled agent run
	// feed the same loop. Its context decorator binds wake to this runner, so a wake from the
	// TUI session resumes IT (the shared wake tool reads its resume from the turn ctx).
	var runner *session.Runner
	runner = session.NewRunner(sess,
		session.WithAgentRunner(agentRun),
		session.WithContextDecorator(func(c context.Context) context.Context {
			return wakecap.WithResume(c, func(note string) { runner.Submit(session.SourceWake, note) })
		}))
	runner.Start(ctx)

	// The chat manager backs the companion app's several named, persisted chats — separate
	// live sessions over the SAME workspace parts (guard/tools/brain/grants). The TUI stays
	// on its single session above; the app browses/opens chats here.
	chatStore := chat.LoadStore(filepath.Join(wsDir, "chats"))
	// onActivity badges this workspace's background chats to any connected app; nil under the
	// TUI (no hub), so the manager stays silent there.
	var onActivity func(chatID, kind string)
	if sh.activity != nil {
		onActivity = func(chatID, kind string) { sh.activity.emit(wsName, chatID, kind) }
	}
	chatMgr := chat.NewManager(ctx, chat.Deps{
		Brain:      w.Brain(),
		Tools:      w.Tools(),
		Guard:      w.Guard(),
		Grants:     w.Grants(),
		Persona:    w.Persona,
		Store:      chatStore,
		AgentRun:   agentRun,
		OnActivity: onActivity,
	})

	// wake: a running turn schedules its OWN resume after a delay (self-paced loops / polling);
	// ungated, bounded (delay clamp + pending cap). One workspace-shared Waker serves every
	// session here — each wake reads its resume from the calling turn's ctx (set by that
	// runner's decorator above, and by each chat's decorator in the manager), so it resumes
	// the chat that invoked it. Cancelled on Reset (via the chatModel).
	waker := wakecap.New()
	w.Tools().Add(waker.Tool())

	sched, err := agent.NewScheduler(w.Agents(), func(runCtx context.Context, def agent.Agent) error {
		// A background (unattended) run stamps NO activity sink → silent by construction.
		// Label it with its workspace so an out-of-band ask reads "[work] …".
		runCtx = gateway.WithLabel(runCtx, wsName)
		_, err := w.RunAgent(runCtx, def.Name, "Run your scheduled task now.")
		return err
	}, agent.WithLog(func(line string) { sh.send(schedulerMsg("[" + wsName + "] " + line)) }))
	if err != nil {
		return nil, err
	}

	return &bound{ws: w, session: sess, runner: runner, chats: chatMgr, waker: waker, scheduler: sched}, nil
}
