// The composition root: assemble the security stack (policy → guard → net →
// registry → brain → agent → ntfy/HITL) and run the bubbletea program. Kept out
// of tui.go so the view logic stays separate from the wiring.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/joho/godotenv"

	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/approval"
	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/filecap"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/llm"
	"github.com/efuturetoday/nocturn/internal/netcap"
	"github.com/efuturetoday/nocturn/internal/script"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/skill"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// tuiNotifier bridges HITL approval into the TUI.
type tuiNotifier struct {
	p       *tea.Program
	resolve func(token string) error
}

func (n *tuiNotifier) Notify(intent string, options []hitl.Option) error {
	reply := make(chan string)
	n.p.Send(approvalMsg{intent: intent, options: options, reply: reply})
	return n.resolve(<-reply)
}

func tuiCmd(_ []string) error {
	_ = godotenv.Load()
	baseURL, apiKey := os.Getenv("FREELLM_BASE_URL"), os.Getenv("FREELLM_API_KEY")
	modelName := os.Getenv("FREELLM_MODEL")
	if apiKey == "" {
		return errors.New("FREELLM_API_KEY not set (see .env / .env.example)")
	}
	if modelName == "" {
		modelName = "auto"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// The workspace is the portable, versionable unit of state (ADR-10). The model
	// inhabits ONLY <ws>/mnt/ (filecap Root + the sandbox /work mount); skills/,
	// grants.json, and secrets.age are host-managed siblings OUTSIDE that mount, so
	// the model can neither see nor write them — a structural control-plane/
	// data-plane split.
	const wsDir = "workspaces/default"

	// Unlock (or, first run, create) the encrypted secret vault — ALL secret
	// material, OAuth refresh tokens included, lives age-encrypted in
	// <ws>/secrets.age; the workspace on disk carries only ciphertext and stays
	// committable. Prompted on the plain terminal, before bubbletea.
	vault, err := unlockVault(filepath.Join(wsDir, "secrets.age"))
	if err != nil {
		return err
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	notifier := &tuiNotifier{}
	// Serialize approvals: tool calls may run concurrently, but the human sees one
	// prompt at a time (auto-allowed effects never reach the notifier and stay parallel).
	engine := hitl.NewEngine(key, hitl.Serialize(notifier))
	notifier.resolve = engine.Resolve

	epochs := capability.NewEpochRegistry()
	store := vault.Store()
	// Host-owned credential injection: a Bearer for gmail.googleapis.com, resolved
	// through a source registered by wireGoogleCredential below (a refreshing OAuth
	// token if configured). The guest never sees the token — it is stamped in only
	// at the gateway boundary, for this destination.
	inj := secret.NewInjector(store, secret.Binding{
		Secret: googleCredentialName, Capability: "http", Host: "gmail.googleapis.com",
		Header: "Authorization", Prefix: "Bearer ",
	})
	scanner := secret.NewScanner(store)
	netCap := &netcap.Net{
		Guard: &gateway.Guard{
			// Base policy on the WIRKUNG axis: reads run still, writes ask — for every
			// family at once (§3). Reach is bounded per caller by cages + grants.
			Policy: capability.Policy{Rules: []capability.Rule{
				{Family: capability.Wildcard, TargetGlob: capability.Wildcard, Writes: capability.MatchRead, Effect: capability.Allow, Epoch: capability.Permanent},
				{Family: capability.Wildcard, TargetGlob: capability.Wildcard, Writes: capability.MatchWrite, Effect: capability.Ask, Epoch: capability.Permanent},
			}},
			Approvals: engine,
			Epochs:    epochs, // shared with the session, so "Allow this session" grants are revocable
			TTL:       2 * time.Minute,
		},
		Credentials: inj,
		Scanner:     scanner,
		HTTP:        &http.Client{Timeout: 15 * time.Second},
	}
	// The filesystem capability group (file.read/file.write), confined to the
	// workspace mount — the second capability family, gated by the same Guard as
	// netcap. The target the broker sees is the mount-relative path, so a
	// grant/cage scopes by path exactly as http scopes by host.
	fileCap := filecap.New(netCap.Guard, filepath.Join(wsDir, "mnt"))

	// One shared Registry dispatches every tool call — the model's AND the
	// script's — so its OnCall observer sees them all in one place, nested by
	// call order.
	reg := tool.NewRegistry(append(netCap.Tools(), fileCap.Tools()...))

	// A script interpreter (QuickJS on the sandbox) exposed as code.run: the
	// model can run multi-step JS that reaches effects via one generic host gate
	// (nocturn.call), dispatched back through the SAME Registry — every effect
	// still passes Guard.Authorize + out-of-band HITL. Pure compute needs no
	// approval. The Timeout is a backstop; the brain's ToolTimeout (via ctx)
	// governs normally. code.run is added after the runner exists so the
	// interpreter can dispatch back into the Registry; a script may not re-enter
	// it (the runner's dispatch refuses code.run).
	runner := script.New(reg)
	runner.Timeout = 60 * time.Second
	reg.Add(runner.Tool())

	// Host-managed OAuth (ADR-5): if Google is configured, run the one-time consent
	// ceremony (prints a URL) and register a refreshing Bearer source for Gmail —
	// before bubbletea grabs the terminal. No-op when unconfigured.
	if err := wireGoogleCredential(ctx, inj, vault); err != nil {
		return err
	}

	// The approved-record (control-plane, model-unreachable) lets an UNCHANGED
	// plugin/MCP server install/connect without re-prompting every boot; a changed
	// one re-surfaces with a diff. It gates only the review, never effect authority.
	approvals := approval.Load(filepath.Join(wsDir, "approved.json"))

	// Install sandboxed plugins from <ws>/plugins/ (reviews each cage on stdin,
	// before the TUI). Their tools join the shared registry; effects stay bounded
	// by each plugin's cage + the broker + HITL.
	if err := loadPlugins(ctx, reg, inj, vault, approvals, wsDir); err != nil {
		return err
	}

	// Connect remote MCP servers declared in the workspace control-plane
	// (<ws>/mcp.json), reviewing each on stdin before the TUI. Their
	// tools join the shared registry namespaced <server>.<tool>; every call is
	// a gated http.write to the server's own host (cage-bounded, leak-
	// scanned, credential-injected under owner mcp:<server>).
	if err := loadMCP(ctx, reg, netCap.Guard, inj, scanner, vault, approvals, wsDir); err != nil {
		return err
	}

	// Skills (agentskills.io): host-side procedural knowledge from <ws>/skills/,
	// surfaced to the model as the single skill.load meta-tool (catalog in its
	// description, name constrained to an enum), whose body loads on demand. Skills
	// are CONTEXT, not tools, and carry zero authority — every effect they steer
	// toward still passes the broker + HITL. Registered only if a visible skill exists.
	skills := skill.Discover([]skill.Scope{{Dir: filepath.Join(wsDir, "skills"), Location: "workspace"}})
	reportSkills(skills) // FYI summary + diagnostics, before the TUI (no prompt — skills carry no authority)
	if lt, ok := skills.LoadTool(); ok {
		reg.Add(lt)
	}
	if skills.Len() > 0 {
		// skill.read serves any loaded skill's bundled files (incl. a /name-only,
		// model-invocation:never skill), so it registers whenever any skill exists.
		reg.Add(skills.ReadTool())
	}

	// Workspace agents (<ws>/agents/*.md): host-side control-plane, validated here
	// and run from the TUI with /<name> <task>. Each is a prompt + the tools it may
	// use + when it runs; the model cannot author them (outside its mount, ADR-10).
	agentsDir := filepath.Join(wsDir, "agents")
	agentDefs, err := agent.LoadAgents(agentsDir)
	if err != nil {
		return err
	}
	reportAgents(agentDefs)

	var p *tea.Program
	reg.OnCall = func(ev tool.Event) { p.Send(toolEventMsg(ev)) }
	b := &brain.Brain{
		Model:       llm.New(baseURL, apiKey, modelName),
		Registry:    reg,
		ToolTimeout: 20 * time.Second,
		OnToken:     func(tok string) { p.Send(tokenMsg(tok)) },
	}
	// Durable "always" grants live IN the workspace (ADR-10): per-workspace,
	// portable, versionable — and host-managed, outside the model's mount, so the
	// model can never write itself a standing grant. Missing file = none.
	// The interactive session's own "always" grants (the workspace-root file). Each
	// AGENT gets its OWN store inside its folder (built per run below) — strict
	// per-owner isolation, no cross-owner sharing (KONZEPT §9).
	sessionGrants := agent.LoadGrantsStore(filepath.Join(wsDir, "grants.json"))
	session := agent.New(b, netCap.Guard, epochs, sessionGrants)
	defer session.Close()
	startTurn := func(input string) context.CancelFunc {
		turnCtx, cancel := context.WithCancel(ctx)
		go func() {
			_, err := session.Ask(turnCtx, input)
			p.Send(doneMsg{err: err})
		}()
		return cancel
	}
	// Running an agent reuses the SAME brain (so its token stream + tool events flow
	// to the TUI like a normal turn) but through agent.RunTask: a fresh epoch + the
	// agent's own grant set + budget + a registry limited to its tools. The model
	// checks the name is a real agent before calling, so the lookup always resolves.
	startAgent := func(name, task string) context.CancelFunc {
		var def agent.Definition
		for _, d := range agentDefs {
			if d.Name == name {
				def = d
				break
			}
		}
		// This agent's OWN durable grants live inside its folder — never the session's.
		agentStore := agent.LoadGrantsStore(agent.GrantsPath(agentsDir, name))
		turnCtx, cancel := context.WithCancel(ctx)
		go func() {
			_, err := agent.RunTask(turnCtx, b, epochs, agentStore, def, task)
			p.Send(doneMsg{err: err})
		}()
		return cancel
	}

	// Detect the terminal background ONCE, here, before bubbletea takes over stdin
	// — so glamour never re-queries it mid-run (that OSC response would leak into
	// the input on every resize). Mouse motion enables wheel scrolling in the
	// viewport; the model also filters stray SGR mouse reports (which some
	// terminals emit at the scroll edge) so they never land in the input.
	dark := lipgloss.HasDarkBackground()
	p = tea.NewProgram(newChatModel(startTurn, startAgent, agentDefs, session.Reset, skills, session.MarkSkill, modelName, dark), tea.WithAltScreen(), tea.WithMouseCellMotion())
	notifier.p = p
	_, err = p.Run()
	return err
}
