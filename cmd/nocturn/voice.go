package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/coder/websocket"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gemini"
	"github.com/efuturetoday/nocturn/agentkit/openai"
	"github.com/efuturetoday/nocturn/internal/voice"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// The voice PoC harness: a browser microphone in, Gemini Live in the middle, the workspace's caged
// tools on the side. It exists to answer the questions that only show up in a real conversation —
// how a gated tool feels mid-sentence, whether the read-only cage is enough, what the round trip
// through this daemon actually costs in latency — before any firmware exists.
//
// It is deliberately NOT part of `serve`: no pairing, no bearer, no device registry, no hub. That
// is safe for exactly one reason, and the reason is the bind address below.

//go:embed voice.html
var voicePage []byte

// loopback is the only address this harness may listen on, and it is not configurable.
//
// The endpoint has no authentication, and the session it opens can read files and fetch URLs
// through the voice cage. On 0.0.0.0 that is unauthenticated read access to the workspace for
// anyone on the network, plus spend on the API key. Loopback is also what the browser needs
// anyway: getUserMedia requires a secure context, and http://127.0.0.1 qualifies where a LAN IP
// does not.
//
// When a real satellite arrives it is on the network, and then this endpoint needs a bearer AND a
// device class that cannot answer approvals: hitl's broker takes the FIRST answer, so a screenless
// device able to approve would outrace the phone it exists to defer to. Do not make this a flag.
const loopback = "127.0.0.1"

// wsReadLimit bounds one frame. Gemini's audio arrives base64'd inside JSON, which blows past
// coder/websocket's 32 KiB default and would close the connection mid-sentence.
const wsReadLimit = 4 << 20

// cmdVoice runs the harness. It returns a Unix exit code.
func cmdVoice(port int, wsName string) int {
	apiKey := os.Getenv("GEMINI_API_KEY")
	model := os.Getenv("GEMINI_LIVE_MODEL")
	if apiKey == "" || model == "" {
		fmt.Fprintln(os.Stderr, "set GEMINI_API_KEY and GEMINI_LIVE_MODEL (or a .env)")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	log := newLogger(os.Stderr)
	ws, err := openWorkspace(wsName, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nocturn voice:", err)
		return 1
	}
	defer ws.Close()

	live := gemini.New(dialGemini, apiKey, model, gemini.WithLogger(agentkit.SlogLogger(log)))
	driver := ws.Voice(live)

	addr := fmt.Sprintf("%s:%d", loopback, port)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /voice", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(voicePage)
	})
	mux.HandleFunc("/voice/ws", func(w http.ResponseWriter, r *http.Request) {
		serveVoiceSocket(w, r, driver, log)
	})

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	log.Info("voice harness listening — loopback only, no auth",
		"url", "http://"+addr+"/voice", "ws", ws.Name(), "tools", ws.VoiceTools())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "nocturn voice:", err)
		return 1
	}
	return 0
}

// openWorkspace assembles the named workspace for the harness. The LLM endpoint is optional here:
// the voice cage holds no sub-agent tool, so nothing in a spoken session reaches a text model.
func openWorkspace(name string, log *slog.Logger) (*workspace.Workspace, error) {
	var llm agentkit.LLM
	if base, key := os.Getenv("FREELLM_BASE_URL"), os.Getenv("FREELLM_API_KEY"); base != "" || key != "" {
		model := os.Getenv("FREELLM_MODEL")
		if model == "" {
			model = "auto"
		}
		llm = openai.New(base, key, model)
	}
	return workspace.Open(
		workspace.Host{LLM: llm, Log: log},
		name,
		filepath.Join(wsRoot, name),
	)
}

// serveVoiceSocket upgrades one browser connection and runs a session on it.
func serveVoiceSocket(w http.ResponseWriter, r *http.Request, driver *voice.Driver, log *slog.Logger) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"localhost*", "127.0.0.1*"}})
	if err != nil {
		log.Warn("voice: upgrade failed", "err", err)
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(wsReadLimit)

	log.Info("voice session opened")
	// Nothing is seeded: the harness starts a fresh conversation each time, so a measurement is
	// never coloured by whatever the last one talked about.
	msgs, err := driver.Run(r.Context(), &browserDevice{conn: conn, ctx: r.Context()}, nil)
	if err != nil {
		log.Warn("voice session ended", "err", err)
	}
	log.Info("voice session closed", "messages", len(msgs))
	for _, m := range msgs {
		log.Info("transcript", "role", m.Role, "text", m.Content)
	}
}

// browserDevice is the satellite end over a browser WebSocket: binary frames carry PCM in both
// directions, text frames carry the one control signal.
//
// Play and Interrupt are called only from the driver's event loop — a single goroutine — which is
// what makes them safe without a write mutex (coder/websocket forbids concurrent writes). Recv runs
// on the driver's separate microphone pump, and reading concurrently with writing is allowed.
type browserDevice struct {
	conn *websocket.Conn
	ctx  context.Context
}

// Recv returns the next microphone chunk, skipping the text frames the page may send.
func (d *browserDevice) Recv(ctx context.Context) ([]byte, error) {
	for {
		typ, data, err := d.conn.Read(ctx)
		if err != nil {
			return nil, err
		}
		if typ == websocket.MessageBinary {
			return data, nil
		}
	}
}

// Play forwards model speech verbatim. No resampling: Gemini emits 24 kHz and the page opens its
// output AudioContext at exactly that rate. A fixed-rate satellite is where resampling belongs.
func (d *browserDevice) Play(pcm []byte) error {
	return d.conn.Write(d.ctx, websocket.MessageBinary, pcm)
}

// Interrupt tells the page to drop everything it has queued but not yet played.
func (d *browserDevice) Interrupt() error {
	return d.conn.Write(d.ctx, websocket.MessageText, []byte(`{"type":"interrupt"}`))
}

// dialGemini is the coder/websocket-backed Transport the adapter dials through. It lives here, at
// the composition root, so internal/voice stays provider-blind and agentkit/gemini stays free of
// third-party dependencies.
func dialGemini(ctx context.Context, url string) (gemini.Transport, error) {
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(wsReadLimit)
	return &geminiSocket{conn: conn}, nil
}

type geminiSocket struct{ conn *websocket.Conn }

func (g *geminiSocket) Send(ctx context.Context, frame []byte) error {
	return g.conn.Write(ctx, websocket.MessageText, frame)
}

func (g *geminiSocket) Recv(ctx context.Context) ([]byte, error) {
	_, data, err := g.conn.Read(ctx)
	return data, err
}

func (g *geminiSocket) Close() error { return g.conn.CloseNow() }
