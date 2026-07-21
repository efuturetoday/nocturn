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

	mux := http.NewServeMux()
	mux.HandleFunc("/pair", func(w http.ResponseWriter, r *http.Request) { handlePair(w, r, devices, log) })
	mux.HandleFunc("/join", func(w http.ResponseWriter, r *http.Request) { handleJoin(w, r, devices, log) })
	mux.HandleFunc("/join/confirm", func(w http.ResponseWriter, r *http.Request) { handleJoinConfirm(w, r, devices, log) })
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
		newConn(ws, spaces, devices, broker, log.With("remote", r.RemoteAddr)).serve(r.Context())
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
