package serve

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/efuturetoday/nocturn/app/auth"
	"github.com/efuturetoday/nocturn/app/hitl"
	"github.com/efuturetoday/nocturn/app/workspace"
)

// bootstrapTTL is how long a first-device pairing code stays valid.
const bootstrapTTL = 5 * time.Minute

// Serve runs a WebSocket daemon at addr exposing space's chats until ctx is cancelled. Every /ws
// connection must carry a paired device's bearer; the first device pairs via POST /pair with the
// bootstrap code logged at startup, further devices via POST /join + /join/confirm. One backend,
// many fronts — the backend is the truth.
func Serve(ctx context.Context, addr string, spaces map[string]*workspace.Workspace, devices *auth.Store, broker *hitl.Broker, log *slog.Logger) error {
	if code := devices.Bootstrap(bootstrapTTL); code != "" {
		log.Info("no devices paired — pair one with POST /pair", "code", code, "validFor", bootstrapTTL)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/pair", func(w http.ResponseWriter, r *http.Request) { handlePair(w, r, devices, log) })
	mux.HandleFunc("/join", func(w http.ResponseWriter, r *http.Request) { handleJoin(w, r, devices, log) })
	mux.HandleFunc("/join/confirm", func(w http.ResponseWriter, r *http.Request) { handleJoinConfirm(w, r, devices, log) })
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		bearer := bearerOf(r)
		if !devices.Verify(bearer) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			log.Warn("ws unauthorized", "remote", r.RemoteAddr)
			return
		}
		devices.UpdateLastUsed(bearer)
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			OriginPatterns: []string{"*"}, // dev: the companion app's origin is not fixed
		})
		if err != nil {
			log.Warn("ws accept failed", "err", err, "remote", r.RemoteAddr)
			return
		}
		defer ws.CloseNow()
		newConn(ws, spaces, devices, broker, log.With("remote", r.RemoteAddr)).serve(r.Context())
	})

	srv := &http.Server{Addr: addr, Handler: mux}
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
