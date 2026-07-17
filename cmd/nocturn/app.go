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
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/joho/godotenv"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/hitl/ntfy"
	"github.com/efuturetoday/nocturn/internal/llm"
	"github.com/efuturetoday/nocturn/internal/notifycap"
	"github.com/efuturetoday/nocturn/internal/session"
)

// consolePusher is the attended fallback for notify() when no out-of-band channel
// (ntfy) is configured: it surfaces the notification as a dim inline TUI line
// instead of a phone push. It satisfies notifycap.Pusher.
type consolePusher struct{ send func(tea.Msg) }

var _ notifycap.Pusher = consolePusher{}

func (c consolePusher) Push(_ context.Context, title, message string) error {
	line := message
	if title != "" {
		line = title + ": " + message
	}
	c.send(notifyMsg(line))
	return nil
}

// tuiNotifier bridges HITL approval into the TUI. It is the default (global) inline
// channel: one prompt on the single bubbletea program, blocking until answered.
type tuiNotifier struct {
	p       *tea.Program
	resolve func(token string) error
}

func (n *tuiNotifier) Notify(intent string, options []hitl.Option) error {
	reply := make(chan string)
	n.p.Send(approvalMsg{intent: intent, options: options, reply: reply})
	return n.resolve(<-reply)
}

// attendedNotifier is the ATTENDED pipe: it surfaces an approval on the specific
// session's own event stream (the session.ApprovalSink the turn carried on its ctx),
// instead of the single global inline prompt. This is what lets a multi-session
// daemon ask on the right client, and lets an attached child agent's approval appear
// in the parent chat where its activity already streams.
//
// PresentApproval returns immediately (it only surfaces the request + records how to
// enact it); the hitl.Engine then blocks on its own pending channel until the human's
// choice comes back through apply → resolve(token). The signed tokens stay host-side —
// the sink only ever sees labels.
type attendedNotifier struct {
	sink    session.ApprovalSink
	resolve func(token string) error
}

func (n attendedNotifier) Notify(intent string, options []hitl.Option) error {
	labels := make([]string, len(options))
	for i, o := range options {
		labels[i] = o.Label
	}
	n.sink.PresentApproval(intent, labels, func(choice int) {
		if choice >= 0 && choice < len(options) {
			_ = n.resolve(options[choice].Token) // enacts the chosen option; unblocks the parked Request
			return
		}
		_ = n.resolve(denyToken(options)) // Esc / out-of-range = deny (fail-closed)
	})
	return nil
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
	var notifyPush notifycap.Pusher
	if reqTopic != "" && respTopic != "" {
		pub := ntfy.New(ntfyBase, reqTopic, ntfyBase+"/"+respTopic, pubOpts...)
		oob = hitl.Serialize(pub)
		notifyPush = pub // fire-and-forget notify() goes to the same user channel
	}

	// One HITL engine for ALL workspaces (it is workspace-agnostic — routes by
	// autonomy, not workspace). Serialized so the human sees one prompt at a time.
	// The router picks the channel per request:
	//   1. attended pipe — if the turn carries a session.ApprovalSink (an interactive
	//      session, or an attached child that inherited it), surface the request on
	//      THAT session's own event stream, so a multi-session daemon asks on the
	//      right client instead of a single global inline prompt;
	//   2. out-of-band — an unattended (scheduled) run with an ntfy channel goes to
	//      the phone;
	//   3. default — the inline TUI prompt (notifier), used when neither applies.
	var approvals *hitl.Engine
	approvals = hitl.NewEngine(key, hitl.Serialize(notifier), hitl.WithRouter(func(rctx context.Context) hitl.Notifier {
		if sink := session.ApprovalSinkFrom(rctx); sink != nil {
			return attendedNotifier{sink: sink, resolve: approvals.Resolve}
		}
		if oob != nil && capability.AutonomyFrom(rctx) != capability.AutonomyAttended {
			return oob
		}
		return nil
	}))
	notifier.resolve = approvals.Resolve
	if oob != nil {
		go func() { _ = ntfy.NewListener(ntfyBase, respTopic, approvals.Resolve, lisOpts...).Run(ctx) }()
		fmt.Printf("Out-of-band approvals via ntfy %s (req=%s, resp=%s)\n", ntfyBase, reqTopic, respTopic)
	}

	// The shared spine handed to every stack. `p` is bound after the stacks exist, so
	// send captures it late.
	var p *tea.Program
	sh := shared{
		master:    master,
		approvals: approvals,
		llmModel:  llm.New(baseURL, apiKey, modelName),
		notify:    notifyPush,
		send:      func(m tea.Msg) { p.Send(m) },
		modelName: modelName,
	}
	if sh.notify == nil {
		// No out-of-band channel configured: notify() falls back to a dim inline TUI line.
		sh.notify = consolePusher{send: sh.send}
	}

	// Build ALL workspaces (the active one ∪ every directory under workspaces/). Each
	// gets its OWN isolated stack; a fresh active name is created on first build.
	names, err := discoverWorkspaces("workspaces")
	if err != nil {
		return err
	}
	if !slices.Contains(names, activeName) {
		names = append(names, activeName)
	}
	workspaces := make(map[string]*bound, len(names))
	for _, name := range names {
		fmt.Printf("Workspace: %s\n", name)
		bw, err := buildStack(ctx, sh, name, filepath.Join("workspaces", name))
		if err != nil {
			return fmt.Errorf("workspace %s: %w", name, err)
		}
		workspaces[name] = bw
		defer bw.session.Close()
	}
	active := workspaces[activeName]

	// Detect the terminal background ONCE, before bubbletea takes over stdin. The TUI
	// opens on the active workspace; /ws switches among all built workspaces.
	dark := lipgloss.HasDarkBackground()
	p = tea.NewProgram(newChatModel(active, workspaces, names, modelName, dark, sh.send), tea.WithAltScreen(), tea.WithMouseCellMotion())
	notifier.p = p

	// Start EVERY workspace's scheduler — one instance fires all workspaces' cron
	// agents (each through its own isolated stack; log lines are workspace-tagged).
	// Iterate the sorted names (not the map) so startup logs are deterministic.
	schedCtx, cancelSched := context.WithCancel(ctx)
	defer cancelSched()
	for _, name := range names {
		bw := workspaces[name]
		if s := bw.scheduler.Scheduled(); len(s) > 0 {
			fmt.Printf("Scheduled [%s]: %s\n", bw.ws.Name(), strings.Join(s, ", "))
		}
		bw.scheduler.Start(schedCtx)
	}

	_, err = p.Run()
	return err
}
