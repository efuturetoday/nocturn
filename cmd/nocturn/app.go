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
	store := secret.NewStore()
	// Host-owned credential injection: a Bearer for gmail.googleapis.com, resolved
	// through a source registered by wireGoogleCredential below (a refreshing OAuth
	// token if configured). The guest never sees the token — it is stamped in only
	// at the gateway boundary, for this destination.
	inj := secret.NewInjector(store, secret.Binding{
		Secret: googleCredentialName, Capability: "http.read", Host: "gmail.googleapis.com",
		Header: "Authorization", Prefix: "Bearer ",
	})
	netCap := &netcap.Net{
		Guard: &gateway.Guard{
			Policy: capability.Policy{Rules: []capability.Rule{
				{Capability: "http.read", TargetGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
				{Capability: "http.write", TargetGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
				{Capability: "dns.resolve", TargetGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
				{Capability: "file.read", TargetGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
				{Capability: "file.write", TargetGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
			}},
			Approvals: engine,
			Epochs:    epochs, // shared with the session, so "Allow this session" grants are revocable
			TTL:       2 * time.Minute,
		},
		Credentials: inj,
		Scanner:     secret.NewScanner(store),
		HTTP:        &http.Client{Timeout: 15 * time.Second},
	}
	// The workspace is the portable, versionable unit of state (ADR-10). The model
	// inhabits ONLY <ws>/mnt/ (filecap Root + the sandbox /work mount); .skills/
	// and grants.json are host-managed siblings OUTSIDE that mount, so the model
	// can neither see nor write them — a structural control-plane/data-plane split.
	const wsDir = "workspaces/default"

	// The filesystem capability group (file.read/file.write), confined to the
	// workspace mount — the second capability family, gated by the same Guard as
	// netcap. The target the broker sees is the mount-relative path, so a
	// grant/ceiling scopes by path exactly as http scopes by host.
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
	if err := wireGoogleCredential(ctx, inj); err != nil {
		return err
	}

	// Install sandboxed plugins from ./plugins/ (reviews each ceiling on stdin,
	// before the TUI). Their tools join the shared registry; effects stay bounded
	// by each plugin's ceiling + the broker + HITL.
	if err := loadPlugins(ctx, reg, inj); err != nil {
		return err
	}

	// Skills (agentskills.io): host-side procedural knowledge from <ws>/.skills/,
	// surfaced to the model as the single skill.load meta-tool (catalog in its
	// description, name constrained to an enum), whose body loads on demand. Skills
	// are CONTEXT, not tools, and carry zero authority — every effect they steer
	// toward still passes the broker + HITL. Registered only if a visible skill exists.
	skills := skill.Discover([]skill.Scope{{Dir: filepath.Join(wsDir, ".skills"), Location: "workspace"}})
	if lt, ok := skills.LoadTool(); ok {
		reg.Add(lt)
	}

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
	grants := agent.LoadGrantsStore(filepath.Join(wsDir, "grants.json"))
	session := agent.New(b, netCap.Guard, epochs, grants)
	defer session.Close()
	startTurn := func(input string) context.CancelFunc {
		turnCtx, cancel := context.WithCancel(ctx)
		go func() {
			_, err := session.Ask(turnCtx, input)
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
	p = tea.NewProgram(newChatModel(startTurn, session.Reset, skills, session.MarkSkill, modelName, dark), tea.WithAltScreen(), tea.WithMouseCellMotion())
	notifier.p = p
	_, err := p.Run()
	return err
}
