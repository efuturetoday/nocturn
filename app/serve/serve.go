package serve

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/efuturetoday/nocturn/app/auth"
	"github.com/efuturetoday/nocturn/app/chat"
	"github.com/efuturetoday/nocturn/app/hitl"
	"github.com/efuturetoday/nocturn/app/workspace"
)

// hub is the set of live connections, so a chat change (a turn save, a markRead) can be broadcast to
// every device — the daemon is the truth, the WebSocket a sink.
type hub struct {
	mu    sync.Mutex
	conns map[*conn]struct{}
}

func newHub() *hub { return &hub{conns: map[*conn]struct{}{}} }

func (h *hub) add(c *conn)    { h.mu.Lock(); h.conns[c] = struct{}{}; h.mu.Unlock() }
func (h *hub) remove(c *conn) { h.mu.Lock(); delete(h.conns, c); h.mu.Unlock() }

// broadcast sends msg to every connection without blocking (a slow one drops it and resyncs).
func (h *hub) broadcast(msg any) {
	h.mu.Lock()
	conns := make([]*conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		c.trySend(msg)
	}
}

// bootstrapTTL is how long a first-device pairing code stays valid.
const bootstrapTTL = 5 * time.Minute

// cors wraps a handler to allow any origin (the companion app's origin is not fixed) and answers the
// preflight OPTIONS with 204. Pairing endpoints carry no cookies, so allow-all is safe here.
func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// Serve runs a WebSocket daemon at addr exposing space's chats until ctx is cancelled. Every /ws
// connection must carry a paired device's bearer; the first device pairs via POST /pair with the
// bootstrap code logged at startup, further devices via POST /join + /join/confirm. One backend,
// many fronts — the backend is the truth.
func Serve(ctx context.Context, addr string, spaces map[string]*workspace.Workspace, devices *auth.Store, broker *hitl.Broker, log *slog.Logger) error {
	if code := devices.Bootstrap(bootstrapTTL); code != "" {
		log.Info("no devices paired — pair one with POST /pair", "code", code, "validFor", bootstrapTTL)
	}

	// Broadcast chat activity to every device when any workspace's chat changes.
	broadcast := newHub()
	for name, ws := range spaces {
		ws.OnChatUpdate(func(m chat.Meta) {
			broadcast.broadcast(ChatActivity{Type: "chat.activity", Ws: name, Chat: m})
		})
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/pair", func(w http.ResponseWriter, r *http.Request) { handlePair(w, r, devices, log) })
	mux.HandleFunc("/join", func(w http.ResponseWriter, r *http.Request) { handleJoin(w, r, devices, log) })
	mux.HandleFunc("/join/confirm", func(w http.ResponseWriter, r *http.Request) { handleJoinConfirm(w, r, devices, log) })
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) { handleRegister(w, r, devices, log) })
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			OriginPatterns: []string{"*"}, // dev: the companion app's origin is not fixed
		})
		if err != nil {
			log.Warn("ws accept failed", "err", err, "remote", r.RemoteAddr)
			return
		}
		defer ws.CloseNow()

		// Verify AFTER the upgrade, then close with app code 4401 on a bad bearer: a pre-upgrade
		// HTTP 401 is invisible to the browser WebSocket API, so the client couldn't tell auth
		// failure from a network drop and would reconnect-loop instead of re-pairing.
		bearer := bearerOf(r)
		if !devices.Verify(bearer) {
			log.Warn("ws unauthorized", "remote", r.RemoteAddr)
			_ = ws.Close(4401, "unauthorized")
			return
		}
		devices.UpdateLastUsed(bearer)
		newConn(ws, spaces, devices, broker, broadcast, log.With("remote", r.RemoteAddr)).serve(r.Context())
	})

	srv := &http.Server{Addr: addr, Handler: cors(mux)}
	// Graceful shutdown on cancel; stop() cancels the hook if ListenAndServe returns first, so the
	// watcher never leaks.
	stop := context.AfterFunc(ctx, func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	})
	defer stop()

	log.Info("ws daemon listening", "addr", addr)
	return srv.ListenAndServe()
}
