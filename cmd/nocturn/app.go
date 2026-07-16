// The composition root: assemble the shared spine (master, HITL engine, LLM client,
// ntfy) and build ONE isolated stack per workspace, then run the bubbletea program.
// Kept out of tui.go so the view logic stays separate from the wiring; the
// per-workspace stack construction itself lives in stack.go.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/joho/godotenv"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/hitl/ntfy"
	"github.com/efuturetoday/nocturn/internal/llm"
	"github.com/efuturetoday/nocturn/internal/timecap"
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

var wsNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// resolveWorkspace picks the ACTIVE workspace from the optional first CLI argument,
// defaulting to "default". The name is confined to a safe folder name — no path
// separators or traversal — so `nocturn <name>` can never point outside workspaces/.
func resolveWorkspace(args []string) (name string, err error) {
	name = "default"
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		name = strings.TrimSpace(args[0])
	}
	if !wsNameRe.MatchString(name) {
		return "", fmt.Errorf("invalid workspace name %q (want %s)", name, wsNameRe)
	}
	return name, nil
}

func tuiCmd(args []string) error {
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

	// The ACTIVE workspace (what the TUI opens on); every discovered workspace is also
	// built (below) so its scheduler runs. `nocturn <name>` picks the active one.
	activeName, err := resolveWorkspace(args)
	if err != nil {
		return err
	}

	// ONE master passphrase (asked once) derives every workspace's vault key (HKDF).
	// Shared across all workspaces; the descriptor (salt/verifier) is non-secret.
	master, err := unlockMaster(filepath.Join("workspaces", "master.json"))
	if err != nil {
		return err
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	notifier := &tuiNotifier{}

	// Out-of-band channel (optional): if an ntfy topic pair is configured, an
	// UNATTENDED run's approval is pushed to the phone instead of the console. The
	// engine routes per request (attended → TUI, unattended → phone); with no config
	// the router returns nil and everything asks inline. Topics are public — the HMAC
	// single-use token is the real integrity control.
	ntfyBase := os.Getenv("NTFY_BASE_URL")
	if ntfyBase == "" {
		ntfyBase = "https://ntfy.sh"
	}
	reqTopic, respTopic := os.Getenv("NTFY_REQ_TOPIC"), os.Getenv("NTFY_RESP_TOPIC")
	var pubOpts []ntfy.Option
	var lisOpts []ntfy.ListenerOption
	if tok := os.Getenv("NTFY_TOKEN"); tok != "" { // self-hosted, access-controlled ntfy
		pubOpts = append(pubOpts, ntfy.WithAuth(tok))
		lisOpts = append(lisOpts, ntfy.ListenerWithAuth(tok))
	}
	var oob hitl.Notifier
	if reqTopic != "" && respTopic != "" {
		oob = hitl.Serialize(ntfy.New(ntfyBase, reqTopic, ntfyBase+"/"+respTopic, pubOpts...))
	}

	// One HITL engine for ALL workspaces (it is workspace-agnostic — routes by
	// autonomy, not workspace). Serialized so the human sees one prompt at a time.
	engine := hitl.NewEngine(key, hitl.Serialize(notifier), hitl.WithRouter(func(rctx context.Context) hitl.Notifier {
		if oob != nil && capability.AutonomyFrom(rctx) != capability.AutonomyAttended {
			return oob
		}
		return nil
	}))
	notifier.resolve = engine.Resolve
	if oob != nil {
		go func() { _ = ntfy.NewListener(ntfyBase, respTopic, engine.Resolve, lisOpts...).Run(ctx) }()
		fmt.Printf("Out-of-band approvals via ntfy %s (req=%s, resp=%s)\n", ntfyBase, reqTopic, respTopic)
	}

	// The shared spine handed to every stack. `p` is bound after the stacks exist, so
	// send captures it late.
	var p *tea.Program
	sh := shared{
		ctx:       ctx,
		master:    master,
		engine:    engine,
		llmModel:  llm.New(baseURL, apiKey, modelName),
		timeCap:   timecap.New(),
		send:      func(m tea.Msg) { p.Send(m) },
		modelName: modelName,
	}

	// Build ALL workspaces (the active one ∪ every directory under workspaces/). Each
	// gets its OWN isolated stack; a fresh active name is created on first build.
	names, err := discoverWorkspaces("workspaces")
	if err != nil {
		return err
	}
	if !contains(names, activeName) {
		names = append(names, activeName)
	}
	stacks := make(map[string]*stack, len(names))
	for _, name := range names {
		fmt.Printf("Workspace: %s\n", name)
		st, err := buildStack(sh, name, filepath.Join("workspaces", name))
		if err != nil {
			return fmt.Errorf("workspace %s: %w", name, err)
		}
		stacks[name] = st
		defer st.session.Close()
	}
	active := stacks[activeName]

	// Detect the terminal background ONCE, before bubbletea takes over stdin. The TUI
	// opens on the active workspace; /ws switches among all built stacks.
	dark := lipgloss.HasDarkBackground()
	p = tea.NewProgram(newChatModel(active, stacks, names, modelName, dark), tea.WithAltScreen(), tea.WithMouseCellMotion())
	notifier.p = p

	// Start EVERY workspace's scheduler — one instance fires all workspaces' cron
	// agents (each through its own isolated stack; log lines are workspace-tagged).
	schedCtx, cancelSched := context.WithCancel(ctx)
	defer cancelSched()
	for _, st := range stacks {
		if s := st.scheduler.Scheduled(); len(s) > 0 {
			fmt.Printf("Scheduled [%s]: %s\n", st.name, strings.Join(s, ", "))
		}
		st.scheduler.Start(schedCtx)
	}

	_, err = p.Run()
	return err
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
