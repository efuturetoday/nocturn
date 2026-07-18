package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/coder/websocket"
	"github.com/libp2p/zeroconf/v2"

	"github.com/efuturetoday/nocturn/internal/appserver"
	"github.com/efuturetoday/nocturn/internal/hitl"
)

// serveDefaultAddr is the daemon's default listen address: ALL interfaces, so a phone on
// the same LAN reaches it out of the box (that is the point of the companion app). The MVP
// has NO authentication, so this exposes an unauthenticated control channel to the whole
// network — it is warned about loudly at startup, and pairing/auth is the next hardening
// step before untrusted networks. Override with NOCTURN_SERVE_ADDR (e.g. "127.0.0.1:8765"
// to force loopback-only).
const serveDefaultAddr = ":8765"

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
	// MVP has NO authentication. Loopback is safe (only local processes reach it). Binding
	// to a non-loopback address exposes an UNAUTHENTICATED control channel to the whole
	// network — any device could drive the assistant and approve effects. Allowed for
	// development (a real phone needs the LAN address), but warned loudly; add pairing/auth
	// before using it on an untrusted network.
	if !isLoopbackAddr(addr) {
		fmt.Printf("\n⚠️  WARNING: serving on %s with NO authentication — any device on your\n"+
			"    network can control this assistant and approve effects. Use only on a network\n"+
			"    you trust; set NOCTURN_SERVE_ADDR=127.0.0.1:8765 to force loopback-only.\n\n", addr)
		// Advertise on the LAN so the app finds the daemon without a typed IP. Only meaningful
		// on a real network interface — loopback has nothing to discover.
		if shutdown, err := advertiseMDNS(addr); err != nil {
			fmt.Printf("mDNS advertise failed (the app must connect by IP): %v\n", err)
		} else {
			defer shutdown()
			fmt.Println("Discoverable on the LAN as _nocturn._tcp (Bonjour).")
		}
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

	server := appserver.NewServer(&appWorkspaces{bounds: sp.workspaces, names: sp.names, sync: sp.sync})
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		// InsecureSkipVerify: skip coder/websocket's same-origin check. A companion app
		// connects from a foreign origin (a browser dev server, or a Capacitor webview whose
		// origin is capacitor://localhost) to the daemon's own host:port — always cross-origin.
		// This is consistent with the no-auth MVP posture: the bind-address warning, not an
		// Origin header, is the trust boundary. Revisit when pairing/auth lands.
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
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

// advertiseMDNS announces the daemon on the LAN as `_nocturn._tcp` (Bonjour/DNS-SD) on the
// listen port, so the app can discover it natively without a typed IP. It returns a shutdown
// func to stop advertising; a TXT record carries the WebSocket path. Register-only — the app
// browses with its own native mDNS.
func advertiseMDNS(addr string) (func(), error) {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("mdns: bad port in %q: %w", addr, err)
	}
	instance := "nocturn"
	if h, err := os.Hostname(); err == nil && h != "" {
		instance = "nocturn @ " + h
	}
	server, err := zeroconf.Register(instance, "_nocturn._tcp", "local.", port, []string{"path=/ws"}, nil)
	if err != nil {
		return nil, err
	}
	return server.Shutdown, nil
}

// isLoopbackAddr reports whether a listen address is loopback-only (safe without auth). An
// empty host (":8765" = all interfaces) is NOT loopback.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		return false // all interfaces
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
