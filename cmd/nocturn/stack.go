package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/approval"
	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/filecap"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/netcap"
	"github.com/efuturetoday/nocturn/internal/script"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/skill"
	"github.com/efuturetoday/nocturn/internal/timecap"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// shared is what EVERY workspace stack shares — the process-wide spine. Nothing here
// is workspace-specific: one master key (derives each vault's key), one HITL engine
// (workspace-agnostic; routes by autonomy, not workspace), one stateless LLM client,
// one clock tool, and one sink to the single bubbletea program. Passed to buildStack.
type shared struct {
	ctx       context.Context
	master    *secret.Master
	engine    *hitl.Engine
	llmModel  brain.Model // stateless client, safe to share across stacks
	timeCap   *timecap.Clock
	send      func(tea.Msg) // p.Send, late-bound (p is created after the stacks)
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
	session   *agent.Session // held for Close() at shutdown
	scheduler *agent.Scheduler

	// The chatModel binding surface (rebound on /ws switch):
	startTurn  func(string) context.CancelFunc
	startAgent func(name, task string) context.CancelFunc
	agentDefs  []agent.Definition
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

	vault, err := unlockVault(sh.master, filepath.Join(wsDir, "secrets.vault"), wsName, filepath.Join(wsDir, "secrets.age"))
	if err != nil {
		return nil, err
	}

	// Its OWN epoch registry — shared with THIS guard + session so "Allow this
	// session" grants are revocable, but never across workspaces (each stack's Reset
	// must not touch another's grants).
	epochs := capability.NewEpochRegistry()
	store := vault.Store()
	inj := secret.NewInjector(store, secret.Binding{
		Secret: googleCredentialName, Capability: "http", Host: "gmail.googleapis.com",
		Header: "Authorization", Prefix: "Bearer ",
	})
	scanner := secret.NewScanner(store)
	netCap := &netcap.Net{
		Guard: &gateway.Guard{
			Policy:    basePolicy(),
			Approvals: sh.engine,
			Epochs:    epochs,
			TTL:       2 * time.Minute,
		},
		Credentials: inj,
		Scanner:     scanner,
		HTTP:        &http.Client{Timeout: 15 * time.Second},
	}
	fileCap := filecap.New(netCap.Guard, filepath.Join(wsDir, "mnt"))

	reg := tool.NewRegistry(append(append(netCap.Tools(), fileCap.Tools()...), sh.timeCap.Tools()...))
	reg.OnCall = func(ev tool.Event) { sh.send(toolEventMsg(ev)) }

	runner := script.New(reg)
	runner.Timeout = 60 * time.Second
	reg.Add(runner.Tool())

	if err := wireGoogleCredential(ctx, inj, vault); err != nil {
		return nil, err
	}

	approvals := approval.Load(filepath.Join(wsDir, "approved.json"))
	if err := loadPlugins(ctx, reg, inj, vault, approvals, wsDir); err != nil {
		return nil, err
	}
	if err := loadMCP(ctx, reg, netCap.Guard, inj, scanner, vault, approvals, wsDir); err != nil {
		return nil, err
	}

	skills := skill.Discover([]skill.Scope{{Dir: filepath.Join(wsDir, "skills"), Location: "workspace"}})
	reportSkills(skills)
	if lt, ok := skills.LoadTool(); ok {
		reg.Add(lt)
	}
	if skills.Len() > 0 {
		reg.Add(skills.ReadTool())
	}

	agentsDir := filepath.Join(wsDir, "agents")
	agentDefs, err := agent.LoadAgents(agentsDir)
	if err != nil {
		return nil, err
	}
	reportAgents(agentDefs)

	b := &brain.Brain{
		Model:       sh.llmModel,
		Registry:    reg,
		ToolTimeout: 20 * time.Second,
		OnToken:     func(tok string) { sh.send(tokenMsg(tok)) },
	}
	sessionGrants := agent.LoadGrantsStore(filepath.Join(wsDir, "grants.json"))
	session := agent.New(b, netCap.Guard, epochs, sessionGrants)

	startTurn := func(input string) context.CancelFunc {
		turnCtx, cancel := context.WithCancel(ctx)
		go func() {
			_, err := session.Ask(turnCtx, input)
			sh.send(doneMsg{err: err})
		}()
		return cancel
	}
	startAgent := func(name, task string) context.CancelFunc {
		var def agent.Definition
		for _, d := range agentDefs {
			if d.Name == name {
				def = d
				break
			}
		}
		agentStore := agent.LoadGrantsStore(agent.GrantsPath(agentsDir, name))
		turnCtx, cancel := context.WithCancel(ctx)
		go func() {
			_, err := agent.RunTask(turnCtx, b, epochs, agentStore, def, task)
			sh.send(doneMsg{err: err})
		}()
		return cancel
	}

	// A QUIET registry view for scheduled (background) runs: same tools, but no OnCall
	// observer — so a background firing in one workspace never spills tool events into
	// the interactive chat (its token stream is muted too). Built once per stack.
	quietReg := reg.Select(func(string) bool { return true })
	quietReg.OnCall = nil
	sched, err := agent.NewScheduler(agentDefs, func(runCtx context.Context, def agent.Definition) error {
		// Label this background run with its workspace, so an out-of-band ask on the
		// phone reads "[work] …" instead of a context-free prompt.
		runCtx = gateway.WithLabel(runCtx, wsName)
		qb := *b
		qb.OnToken = nil
		qb.Registry = quietReg
		gstore := agent.LoadGrantsStore(agent.GrantsPath(agentsDir, def.Name))
		_, err := agent.RunTask(runCtx, &qb, epochs, gstore, def, "Run your scheduled task now.")
		return err
	}, agent.WithLog(func(line string) { sh.send(schedulerMsg("[" + wsName + "] " + line)) }))
	if err != nil {
		return nil, err
	}

	return &stack{
		name: wsName, session: session, scheduler: sched, agentDefs: agentDefs,
		startTurn: startTurn, startAgent: startAgent,
		reset: session.Reset, skills: skills, markSkill: session.MarkSkill,
	}, nil
}
