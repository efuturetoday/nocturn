// The composition root: assemble the shared spine (master, HITL engine, LLM client,
// APNs push) and build ONE isolated stack per workspace, then run the bubbletea program.
// Kept out of tui.go so the view logic stays separate from the wiring; the
// per-workspace stack construction itself lives in stack.go.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/joho/godotenv"

	"github.com/efuturetoday/nocturn/internal/appserver"
	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/device"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/llm"
	"github.com/efuturetoday/nocturn/internal/notifycap"
)

// consolePusher is the attended fallback for notify() when no out-of-band channel
// (APNs push) is configured: it surfaces the notification as a dim inline TUI line
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
// session's own event stream (the chat.ApprovalSink the turn carried on its ctx),
// instead of the single global inline prompt. This is what lets a multi-session
// daemon ask on the right client, and lets an attached child agent's approval appear
// in the parent chat where its activity already streams.
//
// PresentApproval returns immediately (it only surfaces the request + records how to
// enact it); the hitl.Engine then blocks on its own pending channel until the human's
// choice comes back through apply → resolve(token). The signed tokens stay host-side —
// the sink only ever sees labels.
type attendedNotifier struct {
	sink    chat.ApprovalSink
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

// Resolved clears the session's parked prompt once the engine's request ends on ANY
// channel — crucial when the answer came out of band (the chat's own Resolve never
// ran), so a reconnecting client's snapshot doesn't show a phantom approval. Idempotent:
// an in-band answer already cleared it.
func (n attendedNotifier) Resolved() { n.sink.ClearPending() }

// teeNotifier presents an approval on BOTH the chat's own stream (in-band, recorded as
// pending so a reconnecting app answers it) AND out of band (the phone) — used when no
// client is watching the chat right now. One HITL token space makes this race-safe: the
// first tap on either channel resolves the single-use pending; a later tap is a harmless
// no-op. If the out-of-band push fails, the request fails closed (no client is watching,
// so the phone was the only reachable human).
type teeNotifier struct {
	inband attendedNotifier
	oob    hitl.Notifier
}

func (n teeNotifier) Notify(intent string, options []hitl.Option) error {
	_ = n.inband.Notify(intent, options) // records the pending prompt on the chat; never errors
	return n.oob.Notify(intent, options) // the reachable human; its error fails the request closed
}

// Resolved forwards to the in-band end so the recorded pending is cleared on resolution.
func (n teeNotifier) Resolved() { n.inband.Resolved() }

// routeApproval picks the HITL channel for one request from its ctx. The rule:
//   - No approval sink on ctx (a detached run with no chat loop): out-of-band if a device can
//     receive it (oobReady), else nil (the engine's front-end fallback).
//   - A sink is present: ALWAYS record the request in-band on that chat's stream (so a
//     reconnecting app answers it from the snapshot). ALSO push out-of-band only when a device
//     is registered (oobReady) AND no app is foreground-active — a foreground app is reachable
//     over the WebSocket, so presence, not the per-chat client count, gates the push.
//
// oobReady reports whether any device can receive a push; active whether any app is foreground.
// resolve is the engine's own Resolve (the in-band notifier hands a chosen token back to it).
// Extracted from the engine wiring so this decision is testable in isolation.
// channelName labels the routed notifier for a log line: which way an approval went.
func channelName(n hitl.Notifier) string {
	switch n.(type) {
	case teeNotifier:
		return "tee(inband+push)"
	case attendedNotifier:
		return "inband"
	case nil:
		return "none(fallback)"
	default:
		return "oob(push)"
	}
}

func routeApproval(rctx context.Context, oob hitl.Notifier, oobReady, active func() bool, resolve func(token string) error) hitl.Notifier {
	ready := oob != nil && oobReady() // a device is registered to receive a push
	sink := chat.ApprovalSinkFrom(rctx)
	if sink == nil {
		if ready {
			return oob
		}
		return nil
	}
	inband := attendedNotifier{sink: sink, resolve: resolve}
	// Push only when a device can receive it AND no app is foreground-active. A foreground app is
	// reachable over the WebSocket for ANY chat (the open chat shows the prompt; another chat gets
	// an approvalPending badge), so presence — not the per-chat client count — decides.
	if ready && !active() {
		return teeNotifier{inband: inband, oob: oob}
	}
	return inband
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
	sync       *syncHub            // client-sync fan-out (badges + list changes); the app server subscribes, the TUI ignores it
	devices    *device.Store       // paired devices + their push tokens (the app-server auth root)
	pairings   *device.Pairings    // in-flight pending pairings (bootstrap + joins)
	presence   *appserver.Presence // foreground/background count → OOB routing (WS vs push)
}

// buildSpine unlocks the master, wires the shared HITL engine (attended → out-of-band →
// fallback), the LLM client and notify channel, and builds every workspace under
// workspaces/ (plus `ensure`, e.g. the active/default one, created on first run). It is
// front-end-agnostic: the caller supplies how lines are surfaced and how an unroutable
// approval is answered.
func buildSpine(ctx context.Context, send func(tea.Msg), fallback hitl.Notifier, log *slog.Logger, ensure string) (*spine, error) {
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

	// The app-server auth root: paired devices (bearer hashes + push tokens) on disk, the
	// in-flight pending pairings in memory, and the foreground/background presence tracker.
	devices := device.Load(filepath.Join("workspaces", "devices.json"))
	pairings := device.NewPairings(nil)
	presence := appserver.NewPresence()

	// Out-of-band channel (optional): native mobile push (APNs) wakes a paired device's phone for
	// an unattended run's approval. Configured via NOCTURN_APNS_* env; absent → push simply off
	// (the front-end fallback applies). The push is only a WAKE — the approve/deny comes back
	// in-app over the authenticated WebSocket, so no return channel is needed.
	var oob hitl.Notifier
	var notifyPush notifycap.Pusher
	var pn *pushNotifier // the APNs channel, or nil when unconfigured
	oobReady := func() bool { return false }
	if sender := buildAPNS(devices); sender != nil {
		pn = &pushNotifier{sender: sender, devices: devices, log: log.With(slog.String("component", "push"))}
		oob = hitl.Serialize(pn)
		notifyPush = pn
		oobReady = pn.Available
		fmt.Println("Out-of-band approvals via APNs push")
	} else {
		log.InfoContext(ctx, "out-of-band push disabled (NOCTURN_APNS_* not fully set)")
	}

	// onAnswer wakes a backgrounded user when their chat produces an answer: an alert push, but
	// ONLY when a push device exists AND no app is foreground-active (a foreground app already
	// gets the turnEnd over the WebSocket). A tap opens the app → reconnect → snapshot.
	onAnswer := func() {
		if pn != nil && !presence.Active() && pn.Available() {
			_ = pn.Push(context.Background(), "Nocturn", "Your answer is ready")
		}
	}

	// One HITL engine for ALL workspaces. The router per request is routeApproval (extracted so
	// it is unit-testable): in-band on the chat's own stream, additionally out-of-band (push) when
	// a device can receive it AND no foreground app is watching, else the front-end's fallback.
	var approvals *hitl.Engine
	approvalLog := log.With(slog.String("component", "approval"))
	approvals = hitl.NewEngine(key, fallback, hitl.WithLogger(approvalLog), hitl.WithRouter(func(rctx context.Context) hitl.Notifier {
		n := routeApproval(rctx, oob, oobReady, presence.Active, approvals.Resolve)
		// One line that explains every "why did (n't) it push": the inputs that decided the channel.
		approvalLog.InfoContext(rctx, "approval route",
			slog.Bool("hasSink", chat.ApprovalSinkFrom(rctx) != nil),
			slog.Bool("active", presence.Active()),
			slog.Bool("oobReady", oobReady()),
			slog.String("channel", channelName(n)))
		return n
	}))

	hub := newSyncHub()
	// A device membership/token change (pair/revoke/register) pushes the fresh device list to
	// every connected app over the sync hub (DomainDevices).
	devices.OnChange = func() { hub.emitList(appserver.DomainDevices, "") }
	sh := shared{
		master:    master,
		approvals: approvals,
		llmModel:  llm.New(baseURL, apiKey, modelName),
		notify:    notifyPush,
		send:      send,
		modelName: modelName,
		sync:      hub,
		log:       log, // was missing → wake/gateway loggers were nil-silenced
		onAnswer:  onAnswer,
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
	return &spine{sh: sh, approvals: approvals, workspaces: workspaces, names: names, sync: hub, devices: devices, pairings: pairings, presence: presence}, nil
}

// closeSessions stops every workspace's live chats (saving each non-empty chat's history and
// revoking its session grants).
func (sp *spine) closeSessions() {
	for _, bw := range sp.workspaces {
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

	// The TUI owns the terminal, so the diagnostic log goes to a FILE (nocturn.log, 0600), never
	// the screen. A failure to open it degrades to a no-op logger — logging must never block the app.
	logw := io.Discard
	if f, ferr := os.OpenFile("nocturn.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); ferr == nil {
		defer f.Close()
		logw = f
	}
	log := newLogger(logw)

	// The TUI's inline prompt is the approval fallback; `p` is bound after the spine, so
	// send captures it late.
	var p *tea.Program
	notifier := &tuiNotifier{}
	sp, err := buildSpine(ctx, func(m tea.Msg) { p.Send(m) }, hitl.Serialize(notifier), log, activeName)
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
