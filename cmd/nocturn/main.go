// Command nocturn is a terminal chat driven by agentkit Sessions, their tools gated by
// agentkit/gate with human approval on the terminal, and conversations persisted and
// multiplexed by internal/chat — all composed per workspace by internal/workspace.
//
// Reads OPENAI_BASE_URL / OPENAI_API_KEY / OPENAI_MODEL (loads .env).
// Run: go run ./cmd/nocturn
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"

	"github.com/joho/godotenv"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/agentkit/gemini"
	"github.com/efuturetoday/nocturn/agentkit/openai"
	"github.com/efuturetoday/nocturn/internal/auth"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/knowledge/embed"
	"github.com/efuturetoday/nocturn/internal/library"
	"github.com/efuturetoday/nocturn/internal/push"
	"github.com/efuturetoday/nocturn/internal/serve"
	"github.com/efuturetoday/nocturn/internal/speaker"
	"github.com/efuturetoday/nocturn/internal/tools"
	"github.com/efuturetoday/nocturn/internal/tui"
	"github.com/efuturetoday/nocturn/internal/tui/logring"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// dataRoot is everything nocturn persists outside a workspace: the workspaces themselves, the device
// registry, the terminal UI's log.
const (
	dataRoot   = "./nocturn-data"
	wsRoot     = dataRoot + "/workspaces"
	devicePath = dataRoot + "/devices.json"
)

// catalogOff is the NOCTURN_CATALOG_URL value that switches the library off entirely. A word, because
// the empty value is now "use the default" and a person who wants no catalog has to be able to say so.
const catalogOff = "off"

func main() {
	_ = godotenv.Load()
	os.Exit(dispatch(os.Args[1:]))
}

// runApp opens every workspace and runs either the interactive terminal assistant (serveAddr == "")
// or the out-of-band WebSocket daemon (serveAddr set). It returns a Unix exit code. Only this path
// needs the LLM endpoint — the light commands (auth, secret, ls) do not.
//
// serveOpts configure the daemon and are ignored by the terminal path, which has no socket to serve.
func runApp(serveAddr string, serveOpts ...serve.Option) int {
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

	// The daemon dies on SIGINT. The chat does not: it clears the terminal's ISIG, so Ctrl+C arrives
	// there as a keystroke that cancels the running TURN, and go-tui handles a real SIGINT itself by
	// stopping its loop. Deriving the app context from the signal here would cancel every session
	// mid-turn behind the UI's back.
	ctx := context.Background()
	if serveAddr != "" {
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
	}

	// Where the diagnostics go is decided by who is watching, and the two answers share no terminal.
	// The daemon writes to stderr, the operator's window into a running backend: tint-tinted on a
	// TTY, JSON when piped. The terminal UI owns the screen, so its diagnostics go to a file and
	// nothing is printed at all — which is what lets the level be INFO rather than the WARN a shared
	// terminal used to force. NOCTURN_LOG still wins either way.
	var logger *slog.Logger
	var ring *logring.Ring
	if serveAddr != "" {
		logger = newLogger(os.Stderr, slog.LevelInfo)
	} else {
		f, err := logFile(dataRoot)
		if err != nil {
			fmt.Fprintln(os.Stderr, "nocturn: log file:", err)
			return 1
		}
		// Discarded deliberately: this runs as the process leaves, the screen is already gone, and
		// the only place a complaint could go is the file that failed to close.
		defer func() { _ = f.Close() }()
		ring = logring.New(2000) // what Ctrl+L shows; the file is the full record
		logger = newLogger(f, slog.LevelInfo, ring)
	}

	llm := openai.New(baseURL, apiKey, model,
		openai.WithEffort(agentkit.Effort(os.Getenv("OPENAI_REASONING_EFFORT"))),
		openai.WithLogger(agentkit.SlogLogger(logger)),
	)

	// The terminal asks in a modal on the screen it owns; the daemon routes approvals out of band to
	// a connected device via the hitl broker, and wakes a backgrounded device with a push (APNs) when
	// none is attached. Both are gate.Approver — the runtime cannot tell them apart.
	var approver gate.Approver
	var broker *hitl.Broker
	var devices *auth.Store
	var embedder *speaker.Embedder
	var notifier tools.Notifier
	var ui tui.Deps
	if serveAddr == "" {
		ui = tui.Deps{
			Feed:     tui.NewFeed(),
			Approver: tui.NewApprover(),
			Ring:     ring,
			Model:    model,
		}
		approver = ui.Approver
		notifier = tui.NewNotifier(ui.Feed) // a proactive notify becomes a notice in the transcript
	} else {
		var err error
		devices, err = auth.New(devicePath)
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
	// AnyActive is nil-tolerant, so this needs no guard: in terminal mode there is no broker and it
	// reports nobody in the foreground, which is exactly right — every notification takes the print
	// path.
	active := broker.AnyActive
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

	// The terminal takes the screen BEFORE any of this runs — opening a workspace does vault work
	// and MCP handshakes over the network, and a second of a live terminal nobody owns is a second
	// of the user's keystrokes being echoed by the shell and lost.
	if serveAddr == "" {
		return runTUI(ctx, ui, host, model)
	}

	spaces, err := workspace.NewRegistry(host, wsRoot)
	if err != nil {
		logger.Error("open workspaces", "err", err)
		return 1
	}
	if serveAddr != "" {
		logger.Info("nocturn daemon starting", "addr", serveAddr, "workspaces", spaces.Len(), "model", model)
		// serve.Serve wires each workspace's chat subscriptions and only then starts its agent
		// schedulers, so a scheduled firing can never race the subscription wiring.
		// Keep the command line's own credential true after the registry is edited from a phone or a
		// browser. Forgetting that row is a legitimate thing to want — it is how a leaked cli-bearer is
		// revoked — but without this it would also disable `nocturn pair` until the next restart, which
		// is the least convenient moment to discover a restart is needed. The daemon stays the only
		// writer of that file; ensureCLICredential is idempotent, so this costs a lookup when nothing
		// relevant changed.
		serveOpts = append(serveOpts, serve.OnDevicesChanged(func() {
			ensureCLICredential(devices, logger)
		}))
		// The curated catalog. On by default, pointed at the one this project publishes, because a
		// library that is empty until the user hosts a JSON file is a library nobody opens twice.
		// Nothing is fetched here — the first request leaves the machine when a device asks for the
		// list — so a daemon whose owner never opens the library still talks to nobody about it.
		//
		// catalogOff is what makes the default overridable in both directions: an empty variable now
		// means "the default", so "no catalog at all" needs a word of its own rather than the absence
		// of one.
		if url := cmp.Or(os.Getenv("NOCTURN_CATALOG_URL"), library.DefaultURL); url != catalogOff {
			serveOpts = append(serveOpts, serve.WithLibrary(
				library.New(library.Source{URL: url}, dataRoot, logger),
			))
			logger.Info("library enabled", "catalog", url)
		}
		if err := serve.Serve(ctx, serveAddr, spaces, devices, broker, embedder, logger, serveOpts...); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("serve", "err", err)
			return 1
		}
		return 0
	}
	return 0
}

// runTUI draws the terminal chat, opening the workspaces behind the first frame. The order inside
// the opener is load-bearing: the UI's sinks must be registered before any session can open,
// because the chat manager snapshots its event sink when a session's pump starts — an agent firing
// first would stream into nothing.
func runTUI(ctx context.Context, ui tui.Deps, host workspace.Host, model string) int {
	err := tui.Run(ctx, ui, func(ctx context.Context) (*workspace.Workspace, error) {
		spaces, err := workspace.NewRegistry(host, wsRoot)
		if err != nil {
			return nil, err
		}
		ws, ok := spaces.Get(workspace.DefaultWorkspace)
		if !ok {
			return nil, fmt.Errorf("workspace %q is missing", workspace.DefaultWorkspace)
		}
		ui.Feed.Attach(ws)
		for _, space := range spaces.Snapshot() {
			go space.StartAgents(ctx)
		}
		host.Log.Info("nocturn chat starting", "workspaces", spaces.Len(), "model", model)
		return ws, nil
	})
	if err == nil {
		return 0
	}
	fmt.Fprintln(os.Stderr, "nocturn:", err)
	if errors.Is(err, tui.ErrNoTerminal) {
		fmt.Fprintln(os.Stderr, "run `nocturn serve` for the daemon")
		return 2
	}
	return 1
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
