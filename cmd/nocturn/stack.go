package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/efuturetoday/nocturn/internal/activity"
	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/approval"
	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/notifycap"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/session"
	"github.com/efuturetoday/nocturn/internal/skill"
	"github.com/efuturetoday/nocturn/internal/wakecap"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// shared is what EVERY workspace stack shares — the process-wide spine. Nothing here
// is workspace-specific: one master key (derives each vault's key), one HITL engine
// (workspace-agnostic; routes by autonomy, not workspace), one stateless LLM client,
// one clock tool, and one sink to the single bubbletea program. Passed to buildStack.
type shared struct {
	ctx       context.Context
	master    *secret.Master
	approvals *hitl.Engine
	llmModel  brain.Model      // stateless client, safe to share across stacks
	notify    notifycap.Pusher // out-of-band push (ntfy) or the attended TUI fallback
	send      func(tea.Msg)    // p.Send, late-bound (p is created after the stacks)
	modelName string
}

// stack is ONE workspace's fully isolated stack: its own vault/injector/guard/
// registry/epochs/grants/session/scheduler. Isolation is structural — a stack's guard
// and injector simply do not contain another workspace's secrets or grants — so
// running N stacks in one process cannot leak across workspaces (the FRAGEN #12
// choice: logical isolation in one process, not memory-hard separate processes). The
// TUI binds to the ACTIVE stack via the six closures/values below; switching rebinds.
type stack struct {
	name      string
	session   *session.Session // held for Close() at shutdown
	scheduler *agent.Scheduler

	// The chatModel binding surface (rebound on /ws switch):
	startTurn  func(string) context.CancelFunc
	startAgent func(name, task string) context.CancelFunc
	agentDefs  []agent.Agent
	reset      func()
	skills     *skill.Index
	markSkill  func(string)
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
	sort.Strings(names)
	return names, nil
}

// buildStack assembles one workspace's isolated stack: unlock its vault (via the
// shared master), its own epochs+guard+injector+registry+session, its plugins/MCP/
// skills/agents, and its own scheduler. This is exactly the per-workspace sequence
// that used to live inline in tuiCmd — extracted so N of them can run in one process
// (and, later, a headless daemon can reuse it without a TUI).
func buildStack(sh shared, wsName, wsDir string) (*stack, error) {
	ctx := sh.ctx

	// The workspace OWNS the composition (broker, tools, skills, agents, loop, grants).
	// This function keeps only the two things a domain package must not hold: interactive
	// provisioning (plugins/MCP/OAuth prompts) and the TUI transport (the activity sink,
	// self-wake, scheduler log) — both wired below over the workspace's exposed parts.
	host := workspace.Host{Master: sh.master, Approvals: sh.approvals, Model: sh.llmModel, Notify: sh.notify}
	unlock := func(dir, name string) (*secret.Vault, error) {
		return unlockVault(sh.master, filepath.Join(dir, "secrets.vault"), name, filepath.Join(dir, "secrets.age"))
	}
	w, err := workspace.Open(host, basePolicy, unlock, wsName, wsDir)
	if err != nil {
		return nil, err
	}
	reportSkills(w.Skills())
	reportAgents(w.Agents())

	// Interactive extensions + credentials wire against the workspace's own parts.
	if err := wireGoogleCredential(ctx, w.Credentials(), w.Vault()); err != nil {
		return nil, err
	}
	approvals := approval.Load(filepath.Join(wsDir, "approved.json"))
	if err := loadPlugins(ctx, w.Tools(), w.Credentials(), w.Vault(), approvals, wsDir); err != nil {
		return nil, err
	}
	if err := loadMCP(ctx, w.Tools(), w.Guard(), w.Credentials(), w.LeakScanner(), w.Vault(), approvals, wsDir); err != nil {
		return nil, err
	}

	sess := w.OpenSession()

	// uiSink is THE activity sink for an attended turn: streamed answer tokens and
	// tool-call start/end events become TUI messages. Stamped onto a turn's ctx below;
	// a background/scheduled run stamps NO sink → silent by construction.
	uiSink := func(e activity.Event) {
		switch ev := e.(type) {
		case activity.Token:
			sh.send(tokenMsg(ev.Text))
		case activity.ToolEvent:
			sh.send(toolEventMsg(ev))
		}
	}

	startTurn := func(input string) context.CancelFunc {
		turnCtx, cancel := context.WithCancel(ctx)
		turnCtx = activity.WithSink(turnCtx, uiSink) // attended turn: tokens + tool events surface in the chat
		go func() {
			_, err := sess.Ask(turnCtx, input)
			sh.send(doneMsg{err: err})
		}()
		return cancel
	}
	startAgent := func(name, task string) context.CancelFunc {
		turnCtx, cancel := context.WithCancel(ctx)
		// An interactively-spawned agent inherits the chat's sink, so its tokens and
		// tool calls nest into the conversation (a detached/scheduled run below does not).
		turnCtx = activity.WithSink(turnCtx, uiSink)
		go func() {
			_, err := w.RunAgent(turnCtx, name, task)
			sh.send(doneMsg{err: err})
		}()
		return cancel
	}

	// wake: the running agent schedules its OWN resume after a delay (self-paced loops /
	// polling); ungated, bounded (delay clamp + pending cap). The resume routes through
	// the TUI event loop (selfWakeMsg) so it serializes with normal turns — that transport
	// is why the wake tool is wired here, not inside the workspace. Cancelled on Reset.
	waker := wakecap.New(func(note string) { sh.send(selfWakeMsg{note: note, start: startTurn}) })
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

	return &stack{
		name: wsName, session: sess, scheduler: sched, agentDefs: w.Agents(),
		startTurn: startTurn, startAgent: startAgent,
		reset:  func() { waker.Cancel(); sess.Reset() }, // new session → drop pending self-wakes
		skills: w.Skills(), markSkill: sess.MarkSkill,
	}, nil
}
