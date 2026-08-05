// Command nocturn is a terminal chat driven by agentkit Sessions, their tools gated by
// agentkit/gate with human approval on the terminal, and conversations persisted and
// multiplexed by internal/chat — all composed per workspace by internal/workspace.
//
// Reads OPENAI_BASE_URL / OPENAI_API_KEY / OPENAI_MODEL (loads .env).
// Run: go run ./cmd/nocturn
package main

import (
	"bufio"
	"cmp"
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
	"github.com/efuturetoday/nocturn/agentkit/gemini"
	"github.com/efuturetoday/nocturn/agentkit/openai"
	"github.com/efuturetoday/nocturn/internal/auth"
	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/knowledge/embed"
	"github.com/efuturetoday/nocturn/internal/push"
	"github.com/efuturetoday/nocturn/internal/serve"
	"github.com/efuturetoday/nocturn/internal/speaker"
	"github.com/efuturetoday/nocturn/internal/tools"
	"github.com/efuturetoday/nocturn/internal/workspace"
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
	baseURL := os.Getenv("OPENAI_BASE_URL")
	apiKey := os.Getenv("OPENAI_API_KEY")
	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = "auto"
	}
	if baseURL == "" && apiKey == "" {
		fmt.Fprintln(os.Stderr, "set OPENAI_BASE_URL / OPENAI_API_KEY / OPENAI_MODEL (or a .env)")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// One stdin reader, shared by the chat loop and the approval prompt: a turn blocks the input
	// loop, so stdin is free while the approver asks.
	stdin := bufio.NewReader(os.Stdin)

	// Logs go to stderr (structured); the terminal UI keeps stdout. In daemon mode, stderr is the
	// operator's window into the running backend. tint-tinted on a TTY, JSON when piped.
	//
	// The default level follows who is watching. Serving, that is an operator, and silence would be
	// the wrong default for a background process. In the terminal chat it is somebody having a
	// conversation, and on one terminal the two streams interleave: an answer ends up with a
	// timestamp glued to it and the approval prompt scrolls away under diagnostics. So the chat is
	// quiet unless something is wrong, and NOCTURN_LOG turns it back up.
	defaultLevel := slog.LevelWarn
	if serveAddr != "" {
		defaultLevel = slog.LevelInfo
	}
	logger := newLogger(os.Stderr, defaultLevel)

	llm := openai.New(baseURL, apiKey, model,
		openai.WithEffort(agentkit.Effort(os.Getenv("OPENAI_REASONING_EFFORT"))),
		openai.WithLogger(agentkit.SlogLogger(logger)),
	)

	// The terminal prompts inline; the daemon routes approvals out of band to a connected device via
	// the hitl broker, and wakes a backgrounded device with a push (APNs) when none is attached.
	var approver gate.Approver
	var broker *hitl.Broker
	var devices *auth.Store
	var embedder *speaker.Embedder
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
		ensureCLICredential(devices, logger)

		// One embedder for the daemon: the checkpoint is tens of megabytes and immutable once parsed,
		// so a second copy per workspace would buy nothing. Absent when unconfigured, and absence is
		// the whole handling — recognition then reports an unknown speaker and everything else runs as
		// it did before.
		if path := os.Getenv("NOCTURN_SPEAKER_MODEL"); path != "" {
			embedder, err = speaker.Open(path)
			if err != nil {
				logger.Error("speaker model", "path", path, "err", err)
				return 1
			}
			logger.Info("speaker recognition enabled", "model", path)
		} else {
			// Absence is a statement, and it belongs in the log as much as its opposite: otherwise an
			// assistant that never mentions who is speaking reads as broken rather than as unconfigured.
			logger.Info("speaker recognition off — set NOCTURN_SPEAKER_MODEL to enable it; " +
				"the whoami tool is not registered until then")
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
	embedCfg, err := embedConfig()
	if err != nil {
		logger.Error("embedding config", "err", err)
		return 1
	}
	if embedCfg.Configured() {
		logger.Info("document search enabled", "model", embedCfg.Model, "dims", embedCfg.Dims)
	}

	// Speech and document search are both opt-in per process. Without a live model, a device asking
	// for a spoken session is told so rather than left waiting for audio that cannot come; without an
	// embedding endpoint, knowledge_search does not exist rather than failing on every call.
	host := workspace.Host{
		LLM: llm, Live: liveModel(logger), Approver: approver, Master: master,
		Notifier: notifier, Active: active, Speaker: embedder, Embed: embedCfg, Log: logger,
	}

	spaces, err := workspace.OpenAll(host, wsRoot)
	if err != nil {
		logger.Error("open workspaces", "err", err)
		return 1
	}
	if serveAddr != "" {
		logger.Info("nocturn daemon starting", "addr", serveAddr, "workspaces", len(spaces), "model", model)
		// serve.Serve wires each workspace's chat subscriptions and only then starts its agent
		// schedulers, so a scheduled firing can never race the subscription wiring.
		if err := serve.Serve(ctx, serveAddr, spaces, devices, broker, embedder, logger); err != nil && !errors.Is(err, http.ErrServerClosed) {
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

// liveModel builds the duplex speech model from GEMINI_*, or nil when it is not configured.
//
// The model id is required rather than defaulted because live-capable ids churn and, more
// importantly, differ in what they support: one without asynchronous function calling stops the
// whole conversation on every tool call, which is unusable once approvals are involved. A stale
// built-in default would fail at connect time, or worse, connect and behave subtly wrong.
func liveModel(log *slog.Logger) agentkit.LiveLLM {
	key, model := os.Getenv("GEMINI_API_KEY"), os.Getenv("GEMINI_LIVE_MODEL")
	if key == "" || model == "" {
		return nil
	}
	log.Info("voice enabled", "model", model)
	return gemini.New(dialGemini, key, model, gemini.WithLogger(agentkit.SlogLogger(log)))
}

// embedConfig resolves where to embed documents, from the environment.
//
//	NOCTURN_EMBED_BASE_URL   endpoint          falls back to OPENAI_BASE_URL
//	NOCTURN_EMBED_API_KEY    key for it        falls back to OPENAI_API_KEY
//	NOCTURN_EMBED_MODEL      embedding model   defaults to "auto" — NEVER OPENAI_MODEL
//	NOCTURN_EMBED_DIMS       vector length     defaults to the adapter's own default
//
// The endpoint and key fall back because one gateway usually serves both, and making somebody repeat
// two values to use a feature is how a feature goes unused. The MODEL deliberately does not:
// OPENAI_MODEL names a CHAT model, and a chat model id handed to /v1/embeddings gets "unknown
// embedding model" at best and something that answers meaninglessly at worst.
func embedConfig() (embed.Config, error) {
	cfg := embed.Config{
		BaseURL: cmp.Or(os.Getenv("NOCTURN_EMBED_BASE_URL"), os.Getenv("OPENAI_BASE_URL")),
		APIKey:  cmp.Or(os.Getenv("NOCTURN_EMBED_API_KEY"), os.Getenv("OPENAI_API_KEY")),
		Model:   cmp.Or(os.Getenv("NOCTURN_EMBED_MODEL"), embed.DefaultModel),
		Dims:    embed.DefaultDims,
	}
	if raw := os.Getenv("NOCTURN_EMBED_DIMS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			// Refused rather than defaulted: a typo would build the index at a length nobody chose, and
			// the only symptom is search that quietly works less well.
			return embed.Config{}, fmt.Errorf("NOCTURN_EMBED_DIMS=%q is not a positive number", raw)
		}
		cfg.Dims = n
	}
	return cfg, nil
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
	defer ws.Close() // stop both managers' reapers + close every live session on exit
	turnDone := make(chan struct{}, 1)

	// The Manager owns the live sessions and drains each one's events; the REPL just prints them
	// (one chat active at a time). No local session ownership.
	mgr.OnEvent(func(_ string, ev agentkit.Event) { renderEvent(ev, turnDone) })
	// Agent runs stream from a separate manager; print them with a marker and never touch turnDone —
	// a background run's TurnEnd must not release a user turn the REPL is waiting on.
	ws.AgentChats().OnEvent(func(id string, ev agentkit.Event) { renderAgentEvent(id, ev) })
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
	id, err := ws.FireAgent(ctx, name, strings.TrimSpace(task))
	if err != nil {
		fmt.Println("agent:", err)
		return
	}
	fmt.Printf("fired %s → run %s (streaming below)\n", name, id)
}

// renderAgentEvent prints an agent run's streamed events with a run marker. Unlike renderEvent it
// signals no turn-done channel: agent runs are background and must not release a user turn.
func renderAgentEvent(id string, ev agentkit.Event) {
	switch e := ev.(type) {
	case agentkit.Token:
		if e.Frame == 0 {
			fmt.Print(e.Text)
		}
	case agentkit.ToolStart:
		fmt.Printf("\n  [agent %s] → %s(%s)\n", id, e.Tool, e.Args)
	case agentkit.TurnEnd:
		if e.Frame == 0 {
			fmt.Printf("\n[agent %s done]\n", id)
		}
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
