package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/coder/websocket"

	"github.com/efuturetoday/nocturn/internal/appserver"
	"github.com/efuturetoday/nocturn/internal/hitl"
)

// serveDefaultAddr is where the daemon listens until pairing/auth lands (2b-3-ii): loopback
// only, so nothing on the LAN can reach it before there is an auth gate.
const serveDefaultAddr = "127.0.0.1:8765"

// serveCmd runs the companion-app daemon: the same workspace spine as the TUI, exposed over
// a WebSocket the app drives (list/open workspaces, chat, answer approvals). It is NOT a
// second binary — `nocturn serve` is a mode of the one binary.
func serveCmd(_ []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	addr := os.Getenv("NOCTURN_SERVE_ADDR")
	if addr == "" {
		addr = serveDefaultAddr
	}

	// No TUI: notify()/scheduler lines go to stdout, and an approval that can reach neither
	// a connected app (attended sink) nor an out-of-band channel is DENIED (fail closed).
	// When push/ntfy IS configured, the router reaches the phone before this fallback — so
	// an approval still works with no app connected.
	sp, err := buildSpine(ctx, logSend, denyNotifier{}, "default")
	if err != nil {
		return err
	}
	defer sp.closeSessions()

	schedCtx, cancelSched := context.WithCancel(ctx)
	defer cancelSched()
	startSchedulers(schedCtx, sp)

	server := appserver.NewServer(&appWorkspaces{bounds: sp.workspaces, names: sp.names})
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		// Derive from the SERVER ctx (not the request's), so a daemon shutdown cancels every
		// live connection; a single client disconnect ends its Handle via a read error.
		_ = server.Handle(ctx, newWSConn(conn))
	})
	httpSrv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-ctx.Done()
		_ = httpSrv.Close()
	}()

	fmt.Printf("nocturn serve: ws://%s/ws — %d workspace(s)\n", addr, len(sp.names))
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// denyNotifier is the daemon's fail-closed approval fallback: with no attended app client
// and no out-of-band channel, an approval cannot be answered, so returning an error from
// Notify makes the engine deny it immediately (hitl.Engine.Request). It is the LAST resort
// — the router reaches an attended client or the phone first when either is available.
type denyNotifier struct{}

func (denyNotifier) Notify(string, []hitl.Option) error {
	return errors.New("no approval channel available (connect the app or configure out-of-band push)")
}

// logSend is the daemon's send sink (the TUI's is p.Send): notify()/scheduler lines print
// to stdout since there is no TUI to render them.
func logSend(m tea.Msg) {
	switch v := m.(type) {
	case schedulerMsg:
		fmt.Println(string(v))
	case notifyMsg:
		fmt.Println("notify:", string(v))
	}
}
