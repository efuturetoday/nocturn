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
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/joho/godotenv"

	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/llm"
	"github.com/efuturetoday/nocturn/internal/script"
	"github.com/efuturetoday/nocturn/internal/secret"
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
	netCap := &gateway.Net{
		Guard: &gateway.Guard{
			Policy: capability.Policy{Rules: []capability.Rule{
				{Capability: "http.read", HostGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
				{Capability: "http.write", HostGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
				{Capability: "dns.resolve", HostGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
			}},
			Approvals: engine,
			Epochs:    epochs, // shared with the session, so "Allow this session" grants are revocable
			TTL:       2 * time.Minute,
		},
		Scanner: secret.NewScanner(store),
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
	// One shared Registry dispatches every tool call — the model's AND the
	// script's — so its OnCall observer sees them all in one place, nested by
	// call order.
	reg := brain.NewRegistry(netCap.Tools())

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

	var p *tea.Program
	reg.OnCall = func(ev brain.ToolEvent) { p.Send(toolEventMsg(ev)) }
	b := &brain.Brain{
		Model:       llm.New(baseURL, apiKey, modelName),
		Registry:    reg,
		ToolTimeout: 20 * time.Second,
		OnToken:     func(tok string) { p.Send(tokenMsg(tok)) },
	}
	session := agent.New(b, netCap.Guard, epochs)
	defer session.Close()
	startTurn := func(input string) context.CancelFunc {
		turnCtx, cancel := context.WithCancel(ctx)
		go func() {
			_, err := session.Ask(turnCtx, input)
			p.Send(doneMsg{err: err})
		}()
		return cancel
	}

	p = tea.NewProgram(newChatModel(startTurn, session.Reset), tea.WithAltScreen())
	notifier.p = p
	_, err := p.Run()
	return err
}
