// Command app is nocturn rebuilt on agentkit: a terminal chat driven by agentkit Sessions, their
// tools gated by agentkit/gate with human approval on the terminal, and conversations persisted and
// multiplexed by app/chat — all composed per workspace by app/workspace. This is the greenfield
// root; the old cmd/nocturn still stands.
//
// Reads FREELLM_BASE_URL / FREELLM_API_KEY / FREELLM_MODEL (loads .env). Run: go run ./app
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/joho/godotenv"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/agentkit/openai"
	"github.com/efuturetoday/nocturn/app/auth"
	"github.com/efuturetoday/nocturn/app/chat"
	"github.com/efuturetoday/nocturn/app/hitl"
	"github.com/efuturetoday/nocturn/app/push"
	"github.com/efuturetoday/nocturn/app/serve"
	"github.com/efuturetoday/nocturn/app/tools"
	"github.com/efuturetoday/nocturn/app/workspace"
)

const wsRoot = "./nocturn-data/workspaces"

func main() {
	_ = godotenv.Load()
	os.Exit(dispatch(os.Args[1:]))
}

// runApp opens every workspace and runs either the interactive terminal assistant (serveAddr == "")
// or the out-of-band WebSocket daemon (serveAddr set). It returns a Unix exit code. Only this path
// needs the LLM endpoint — the light commands (auth, secret, ls) do not.
func runApp(serveAddr string) int {
	baseURL := os.Getenv("FREELLM_BASE_URL")
	apiKey := os.Getenv("FREELLM_API_KEY")
	model := os.Getenv("FREELLM_MODEL")
	if model == "" {
		model = "auto"
	}
	if baseURL == "" && apiKey == "" {
		fmt.Fprintln(os.Stderr, "set FREELLM_BASE_URL / FREELLM_API_KEY / FREELLM_MODEL (or a .env)")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// One stdin reader, shared by the chat loop and the approval prompt: a turn blocks the input
	// loop, so stdin is free while the approver asks.
	stdin := bufio.NewReader(os.Stdin)

	// Logs go to stderr (structured); the terminal UI keeps stdout. In daemon mode, stderr is the
	// operator's window into the running backend. tint-tinted on a TTY, JSON when piped; level via
	// NOCTURN_LOG. See newLogger.
	logger := newLogger(os.Stderr)

	llm := openai.New(baseURL, apiKey, model,
		openai.WithEffort(agentkit.Effort(os.Getenv("FREELLM_REASONING_EFFORT"))),
		openai.WithLogger(agentkit.SlogLogger(logger)),
	)

	// The terminal prompts inline; the daemon routes approvals out of band to a connected device via
	// the hitl broker, and wakes a backgrounded device with a push (APNs) when none is attached.
	var approver gate.Approver
	var broker *hitl.Broker
	var devices *auth.Store
	var notifier tools.Notifier
	if serveAddr == "" {
		approver = &terminalApprover{in: stdin}
		notifier = printNotifier{} // proactive notify prints to the terminal
	} else {
		var err error
		devices, err = auth.New("./nocturn-data/devices.json")
		if err != nil {
			logger.Error("device store", "err", err)
			return 1
		}
		pushLog := logger.With("component", "push")
		sender := apnsSender(pushLog) // nil when APNs is not configured
		broker = hitl.NewBroker(pusherFor(sender, devices, pushLog), logger)
		approver = broker
		notifier = &pushNotifier{devices: devices, sender: sender, log: pushLog}
	}
	master := buildMaster(logger)
	// Proactive messages route by device presence, the same signal that routes approvals. The broker
	// holds it; nil in terminal mode (no daemon), where every notification takes the print path.
	var active func() bool
	if broker != nil {
		active = broker.AnyActive
	}
	host := workspace.Host{LLM: llm, Approver: approver, Master: master, Notifier: notifier, Active: active, Log: logger}

	spaces, err := workspace.OpenAll(host, wsRoot)
	if err != nil {
		logger.Error("open workspaces", "err", err)
		return 1
	}
	if serveAddr != "" {
		logger.Info("nocturn daemon starting", "addr", serveAddr, "workspaces", len(spaces), "model", model)
		// serve.Serve wires each workspace's chat subscriptions and only then starts its agent
		// schedulers, so a scheduled firing can never race the subscription wiring.
		if err := serve.Serve(ctx, serveAddr, spaces, devices, broker, logger); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("serve", "err", err)
			return 1
		}
		return 0
	}

	// Terminal mode has no daemon subscriptions to race against, so start the schedulers here.
	for _, ws := range spaces {
		go ws.StartAgents(ctx)
	}
	run(ctx, spaces[workspace.DefaultWorkspace], stdin, model)
	return 0
}

// apnsSender builds the APNs sender from NOCTURN_APNS_*; nil when unconfigured or on error, so both
// the hitl waker and the notify tool degrade to a log line rather than failing.
func apnsSender(log *slog.Logger) push.Sender {
	sender, err := push.APNSFromEnv()
	if err != nil {
		log.Warn("apns push disabled", "err", err)
		return nil
	}
	if sender == nil {
		return nil // NOCTURN_APNS_KEY unset — push off
	}
	log.Info("apns push enabled")
	return sender
}

// pusherFor builds the out-of-band waker for the hitl broker: a real push when sender is set, else a
// logging placeholder.
func pusherFor(sender push.Sender, devices *auth.Store, log *slog.Logger) hitl.Pusher {
	if sender == nil {
		return hitl.NewLogPusher(log)
	}
	return &pushWaker{devices: devices, sender: sender}
}

// pushNotifier is the notify tool's user-facing sender in daemon mode: it pushes to every paired iOS
// device. A nil sender (APNs unconfigured) degrades to a log line rather than an error.
type pushNotifier struct {
	devices *auth.Store
	sender  push.Sender
	log     *slog.Logger
}

func (p *pushNotifier) Notify(ctx context.Context, n tools.Notification) error {
	if p.sender == nil {
		// Metadata only: APNs being unconfigured is the default in development, so logging the body
		// here would route every reminder's personal text to the log on the common path.
		p.log.Info("notify (no push configured)", "kind", n.Kind, "ws", n.Ws, "len", len(n.Message))
		return nil
	}
	var tokens []string
	for _, t := range p.devices.PushTargets() {
		if t.Platform == "ios" || t.Platform == "" {
			tokens = append(tokens, t.Token)
		}
	}
	if len(tokens) == 0 {
		return nil
	}
	title := n.Title
	if title == "" {
		title = "Nocturn"
	}
	kind := n.Kind
	if kind == "" {
		kind = tools.NotifyKind // the app switches on this; never hand it an empty discriminator
	}
	// kind + ws + chatId let the app label the notification and open the conversation it came from on
	// a tap; the push itself carries no authority, only the message the user already agreed to
	// receive. chatId is omitted when there is nothing to open.
	data := map[string]string{"type": kind, "ws": n.Ws}
	if n.ChatID != "" {
		data["chatId"] = n.ChatID
	}
	return p.sender.Send(ctx, push.Message{Title: title, Body: n.Message, Data: data}, tokens)
}

// printNotifier is the notify tool's terminal fallback: a proactive notification prints inline.
type printNotifier struct{}

func (printNotifier) Notify(_ context.Context, n tools.Notification) error {
	label := n.Kind
	if label == "" {
		label = tools.NotifyKind
	}
	if n.Title != "" {
		fmt.Printf("\n[%s] %s: %s\n", label, n.Title, n.Message)
	} else {
		fmt.Printf("\n[%s] %s\n", label, n.Message)
	}
	return nil
}

// pushWaker bridges the device registry and a push Sender into hitl.Pusher: it wakes every device
// with a registered token so it can foreground and answer the pending approval over the WebSocket.
type pushWaker struct {
	devices *auth.Store
	sender  push.Sender
}

func (p *pushWaker) Push(ctx context.Context, intent string) error {
	var tokens []string
	for _, t := range p.devices.PushTargets() {
		if t.Platform == "ios" || t.Platform == "" {
			tokens = append(tokens, t.Token)
		}
	}
	if len(tokens) == 0 {
		return nil
	}
	return p.sender.Send(ctx, push.Message{
		Title: "Nocturn",
		Body:  intent,
		Data:  map[string]string{"type": "approval"},
	}, tokens)
}

// run is the terminal loop: the first message (or one after /new) starts a chat; /chats lists,
// /open resumes, /quit exits. One chat is active at a time — switching closes the previous session
// (its transcript is already persisted and reloads on resume).
func run(ctx context.Context, ws *workspace.Workspace, stdin *bufio.Reader, model string) {
	mgr := ws.Chats()
	defer mgr.CloseAll() // stop the reaper + close every live session on exit
	turnDone := make(chan struct{}, 1)

	// The Manager owns the live sessions and drains each one's events; the REPL just prints them
	// (one chat active at a time). No local session ownership.
	mgr.OnEvent(func(_ string, ev agentkit.Event) { renderEvent(ev, turnDone) })
	var activeID string

	fmt.Printf("nocturn (model %q) — /chats · /open <id> · /new · /agents · /fire <name> <task> · /quit\n", model)
	fmt.Print("\ntype a message to start a chat.\n")
	for {
		fmt.Print("\n> ")
		line, err := stdin.ReadString('\n')
		if err != nil {
			return // EOF
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		switch {
		case line == "/quit" || line == "/exit":
			return
		case line == "/chats":
			listChats(mgr)
			continue
		case line == "/agents":
			listAgents(ws)
			continue
		case strings.HasPrefix(line, "/fire "):
			fireAgent(ctx, ws, strings.TrimPrefix(line, "/fire "))
			continue
		case line == "/new":
			activeID = "" // the next message starts a fresh chat; the old one keeps living in the Manager
			fmt.Println("new chat — type your first message.")
			continue
		case strings.HasPrefix(line, "/open "):
			activeID = strings.TrimSpace(strings.TrimPrefix(line, "/open "))
			mgr.Open(activeID) // spin/get the live session; its events print via OnEvent
			fmt.Printf("opened %s — type to continue.\n", activeID)
			continue
		}

		// A plain line: start a new chat, or continue the active one — id-addressed, no session owned.
		if activeID == "" {
			activeID, _ = mgr.Start(line)
		} else {
			mgr.Open(activeID).Submit(line)
		}
		select {
		case <-turnDone:
		case <-ctx.Done():
			return
		}
	}
}

func listChats(mgr *chat.Manager) {
	metas, err := mgr.List()
	if err != nil {
		fmt.Println("list:", err)
		return
	}
	if len(metas) == 0 {
		fmt.Println("(no chats yet)")
		return
	}
	for _, m := range metas {
		fmt.Printf("  %s  %-42s  %d turns  %s\n", m.ID, m.Name, m.Turns, m.Updated.Format("Jan 2 15:04"))
	}
}

func listAgents(ws *workspace.Workspace) {
	agents := ws.Agents()
	if len(agents) == 0 {
		fmt.Println("(no agents — add one at agents/<name>/agent.md)")
		return
	}
	for _, a := range agents {
		when := a.When
		if when == "" {
			when = "manual"
		}
		fmt.Printf("  %-16s tools:%v  when:%s\n", a.Name, a.Tools, when)
	}
}

func fireAgent(ctx context.Context, ws *workspace.Workspace, rest string) {
	name, task, _ := strings.Cut(strings.TrimSpace(rest), " ")
	if name == "" {
		fmt.Println("usage: /fire <name> <task>")
		return
	}
	fmt.Printf("firing %s (unattended)…\n", name)
	answer, err := ws.FireAgent(ctx, name, strings.TrimSpace(task))
	if err != nil {
		fmt.Println("agent:", err)
	}
	if answer != "" {
		fmt.Println(answer)
	}
}

// renderEvent prints one streamed event to the terminal and signals done on the top-level TurnEnd.
// The Manager's pump feeds it every live session's events (see OnEvent).
func renderEvent(ev agentkit.Event, done chan<- struct{}) {
	switch e := ev.(type) {
	case agentkit.Token:
		fmt.Print(e.Text)
	case agentkit.Thinking:
		fmt.Printf("\033[2m%s\033[0m", e.Text)
	case agentkit.ToolStart:
		fmt.Printf("\n  → %s(%s)\n", e.Tool, e.Args)
	case agentkit.ToolEnd:
		if e.Err != nil {
			fmt.Printf("  ← %s: %v\n", e.Tool, e.Err)
		}
	case agentkit.TurnEnd:
		if e.Frame != 0 {
			return // a sub-agent turn ended; the user's turn is the top-level one
		}
		if e.Err != nil {
			fmt.Printf("\n[stopped: %v]", e.Err)
		}
		fmt.Printf("\n[tokens: %d]\n", e.Tokens.Total)
		select {
		case done <- struct{}{}:
		default:
		}
	}
}

// terminalApprover asks for approval on the terminal, sharing the chat's stdin reader.
type terminalApprover struct {
	in *bufio.Reader
}

func (t *terminalApprover) Ask(_ context.Context, a gate.Action, suggest []gate.Grant) (bool, gate.Grant, gate.Recall, error) {
	exact := gate.Grant{Kind: a.Kind, Target: a.Target}
	fmt.Print("\n  [approve] " + a.Kind)
	if a.Target != "" {
		fmt.Print(" → " + a.Target)
	}
	fmt.Print(" ? [y=session / a=always")
	for i, s := range suggest {
		fmt.Printf(" / %d=always %s", i+1, s.Target)
	}
	fmt.Print(" / N] ")

	line, _ := t.in.ReadString('\n')
	switch choice := strings.ToLower(strings.TrimSpace(line)); choice {
	case "y":
		return true, exact, gate.RecallSession, nil
	case "a":
		return true, exact, gate.RecallAlways, nil
	default:
		if n, err := strconv.Atoi(choice); err == nil && n >= 1 && n <= len(suggest) {
			return true, suggest[n-1], gate.RecallAlways, nil
		}
		return false, gate.Grant{}, gate.RecallNever, nil
	}
}
