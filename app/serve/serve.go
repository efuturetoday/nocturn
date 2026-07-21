package serve

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/coder/websocket"

	"github.com/efuturetoday/nocturn/app/workspace"
)

// Serve runs a WebSocket daemon at addr exposing space's chats until ctx is cancelled. Every /ws
// connection drives the same workspace — the backend is the truth, fronts attach and observe.
func Serve(ctx context.Context, addr string, space *workspace.Workspace, log *slog.Logger) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			OriginPatterns: []string{"*"}, // dev: the companion app's origin is not fixed
		})
		if err != nil {
			log.Warn("ws accept failed", "err", err, "remote", r.RemoteAddr)
			return
		}
		defer ws.CloseNow()
		newConn(ws, space, log.With("remote", r.RemoteAddr)).serve(r.Context())
	})

	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	log.Info("ws daemon listening", "addr", addr)
	return srv.ListenAndServe()
}
