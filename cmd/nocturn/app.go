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

// spine bundles the process-wide services and every built workspace stack — the shared
// core the interactive TUI and the serve daemon both run on. They differ only in `send`
// (where notify/scheduler lines go) and the approval `fallback` (used when neither an
// attended client nor an out-of-band channel applies): the TUI passes its inline prompt,
// serve a fail-closed deny.
type spine struct {
	sh         shared
	approvals  *hitl.Engine
	workspaces map[string]*bound
	names      []string
}

// buildSpine unlocks the master, wires the shared HITL engine (attended → out-of-band →
// fallback), the LLM client and notify channel, and builds every workspace under
// workspaces/ (plus `ensure`, e.g. the active/default one, created on first run). It is
// front-end-agnostic: the caller supplies how lines are surfaced and how an unroutable
// approval is answered.
func buildSpine(ctx context.Context, send func(tea.Msg), fallback hitl.Notifier, ensure string) (*spine, error) {
	_ = godotenv.Load()
	baseURL, apiKey := os.Getenv("FREELLM_BASE_URL"), os.Getenv("FREELLM_API_KEY")
	modelName := os.Getenv("FREELLM_MODEL")
	if apiKey == "" {
		return nil, errors.New("FREELLM_API_KEY not set (see .env / .env.example)")
	}
	if modelName == "" {
		modelName = "auto"
	}

	// ONE master passphrase (asked once) derives every workspace's vault key (HKDF).
	master, err := unlockMaster(filepath.Join("workspaces", "master.json"))
	if err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}

	// Out-of-band channel (optional): an ntfy topic pair pushes an unattended run's
	// approval to the phone. Topics are public — the HMAC single-use token is the integrity.
	ntfyBase := os.Getenv("NTFY_BASE_URL")
	if ntfyBase == "" {
		ntfyBase = "https://ntfy.sh"
	}
	reqTopic, respTopic := os.Getenv("NTFY_REQ_TOPIC"), os.Getenv("NTFY_RESP_TOPIC")
	var pubOpts []ntfy.Option
	var lisOpts []ntfy.ListenerOption
	if tok := os.Getenv("NTFY_TOKEN"); tok != "" {
		pubOpts = append(pubOpts, ntfy.WithAuth(tok))
		lisOpts = append(lisOpts, ntfy.ListenerWithAuth(tok))
	}
	var oob hitl.Notifier
	var notifyPush notifycap.Pusher
	if reqTopic != "" && respTopic != "" {
		pub := ntfy.New(ntfyBase, reqTopic, ntfyBase+"/"+respTopic, pubOpts...)
		oob = hitl.Serialize(pub)
		notifyPush = pub
	}

	// One HITL engine for ALL workspaces. Router per request: an attended client's own
	// stream (session.ApprovalSink) first; else out-of-band (unless the run is attended);
	// else the front-end's fallback (TUI prompt, or serve's deny).
	var approvals *hitl.Engine
	approvals = hitl.NewEngine(key, fallback, hitl.WithRouter(func(rctx context.Context) hitl.Notifier {
		if sink := session.ApprovalSinkFrom(rctx); sink != nil {
			return attendedNotifier{sink: sink, resolve: approvals.Resolve}
		}
		// No attended client is watching (the app/TUI is not connected, or this is a
		// background run): reach the human OUT-OF-BAND — ntfy today, native push (Phase 3)
		// plugs in here as the same `oob`. This is exactly why an approval still lands on
		// the phone when the app is not connected. Only with NO out-of-band channel at all
		// does it fall through to the front-end fallback (TUI prompt, or serve's deny).
		if oob != nil {
			return oob
		}
		return nil
	}))
	if oob != nil {
		go func() { _ = ntfy.NewListener(ntfyBase, respTopic, approvals.Resolve, lisOpts...).Run(ctx) }()
		fmt.Printf("Out-of-band approvals via ntfy %s (req=%s, resp=%s)\n", ntfyBase, reqTopic, respTopic)
	}

	sh := shared{
		master:    master,
		approvals: approvals,
		llmModel:  llm.New(baseURL, apiKey, modelName),
		notify:    notifyPush,
		send:      send,
		modelName: modelName,
	}
	if sh.notify == nil {
		sh.notify = consolePusher{send: send} // no ntfy: notify() falls back to the send sink
	}

	names, err := discoverWorkspaces("workspaces")
	if err != nil {
		return nil, err
	}
	if ensure != "" && !slices.Contains(names, ensure) {
		names = append(names, ensure)
	}
	workspaces := make(map[string]*bound, len(names))
	for _, name := range names {
		fmt.Printf("Workspace: %s\n", name)
		bw, err := buildStack(ctx, sh, name, filepath.Join("workspaces", name))
		if err != nil {
			return nil, fmt.Errorf("workspace %s: %w", name, err)
		}
		workspaces[name] = bw
	}
	return &spine{sh: sh, approvals: approvals, workspaces: workspaces, names: names}, nil
}

// closeSessions closes every workspace's interactive session (revoking its session grants)
// and stops its multi-chat runners (saving each chat's history).
func (sp *spine) closeSessions() {
	for _, bw := range sp.workspaces {
		bw.session.Close()
		bw.chats.CloseAll()
	}
}

// startSchedulers starts every workspace's cron scheduler under ctx (deterministic order).
func startSchedulers(ctx context.Context, sp *spine) {
	for _, name := range sp.names {
		bw := sp.workspaces[name]
		if s := bw.scheduler.Scheduled(); len(s) > 0 {
			fmt.Printf("Scheduled [%s]: %s\n", bw.ws.Name(), strings.Join(s, ", "))
		}
		bw.scheduler.Start(ctx)
	}
}

func tuiCmd(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// The ACTIVE workspace (what the TUI opens on); every discovered workspace is also
	// built so its scheduler runs. `nocturn <name>` picks the active one.
	activeName, err := resolveWorkspace(args)
	if err != nil {
		return err
	}

	// The TUI's inline prompt is the approval fallback; `p` is bound after the spine, so
	// send captures it late.
	var p *tea.Program
	notifier := &tuiNotifier{}
	sp, err := buildSpine(ctx, func(m tea.Msg) { p.Send(m) }, hitl.Serialize(notifier), activeName)
	if err != nil {
		return err
	}
	notifier.resolve = sp.approvals.Resolve
	defer sp.closeSessions()

	// Detect the terminal background ONCE, before bubbletea takes over stdin.
	dark := lipgloss.HasDarkBackground()
	p = tea.NewProgram(newChatModel(sp.workspaces[activeName], sp.workspaces, sp.names, sp.sh.modelName, dark, sp.sh.send), tea.WithAltScreen(), tea.WithMouseCellMotion())
	notifier.p = p

	schedCtx, cancelSched := context.WithCancel(ctx)
	defer cancelSched()
	startSchedulers(schedCtx, sp)

	_, err = p.Run()
	return err
}
