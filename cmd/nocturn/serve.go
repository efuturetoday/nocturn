package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
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
func serveCmd(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	addr := os.Getenv("NOCTURN_SERVE_ADDR")
	if addr == "" {
		addr = serveDefaultAddr
	}
	// Diagnostic log → stdout (the daemon's event stream; systemd/launchd capture it). serve
	// stdout carries no data pipe, so logs and the startup/QR lines share it.
	log := newLogger(os.Stdout)
	srvLog := log.With(slog.String("component", "server"))

	// Build the process spine — it owns the device store (bearer hashes + push tokens), the
	// in-flight pending pairings, the foreground/background presence tracker, and the HITL engine
	// wired to APNs push. No TUI: notify()/scheduler lines go to stdout, and an approval that can
	// reach neither a connected app nor a push device is DENIED (fail closed).
	sp, err := buildSpine(ctx, logSend, denyNotifier{}, log, "default")
	if err != nil {
		return err
	}
	defer sp.closeSessions()
	defer func() { _ = sp.devices.Flush() }() // persist any LastUsed timestamps coalesced by Touch

	// --reset-pairing is the operator's recovery path — empty the device store so the daemon
	// re-opens the bootstrap pairing below (used when every paired device is lost).
	if slices.Contains(args, "--reset-pairing") {
		if err := sp.devices.Reset(); err != nil {
			return fmt.Errorf("reset pairing: %w", err)
		}
		fmt.Println("device pairings reset — all devices removed.")
	}

	// Advertise on the LAN so the app finds the daemon without a typed IP. Only meaningful on a
	// real interface — loopback has nothing to discover. The channel is bearer-authenticated
	// (below), but still PLAINTEXT: use a trusted LAN until TLS lands (see the pairing plan).
	if !isLoopbackAddr(addr) {
		if shutdown, err := advertiseMDNS(addr); err != nil {
			fmt.Printf("mDNS advertise failed (the app must connect by IP): %v\n", err)
		} else {
			defer shutdown()
			fmt.Println("Discoverable on the LAN as _nocturn._tcp (Bonjour).")
		}
	}

	schedCtx, cancelSched := context.WithCancel(ctx)
	defer cancelSched()
	startSchedulers(schedCtx, sp)

	// Bootstrap: with no paired device, the operator is the trust root. Mint a pending pairing
	// and print its QR + typed OTP to stdout — whoever started this process (and sees this
	// terminal) pairs the first phone. Once a device exists this branch is silent.
	if len(sp.devices.List()) == 0 {
		printBootstrap(addr, sp.pairings.MintBootstrap())
	}

	server := appserver.NewServer(&appWorkspaces{bounds: sp.workspaces, names: sp.names, sync: sp.sync, pairings: sp.pairings, devices: sp.devices}, sp.presence)
	sp.devices.OnRevoke = func(id string) { // unpairing a device drops its live connection at once
		srvLog.InfoContext(ctx, "device revoked", slog.String("device", id))
		server.Revoke(id)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		// The auth gate lives HERE, in cmd, before the upgrade — appserver stays pure (no auth
		// hook). An unknown/absent bearer never reaches Handle.
		dev, ok := sp.devices.Verify(bearerFrom(r))
		if !ok {
			srvLog.WarnContext(r.Context(), "ws rejected: unknown bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		sp.devices.Touch(dev.ID) // record last-used (coalesced to disk, not a write per connection)
		srvLog.InfoContext(ctx, "ws connect", slog.String("device", dev.ID), slog.String("name", dev.Name))
		// InsecureSkipVerify: skip coder/websocket's same-origin check. A companion app
		// connects from a foreign origin (a browser dev server, or a Capacitor webview whose
		// origin is capacitor://localhost) to the daemon's own host:port — always cross-origin.
		// The bearer, not an Origin header, is the trust boundary.
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		// Derive from the SERVER ctx (not the request's), so a daemon shutdown cancels every
		// live connection; a single client disconnect ends its Handle via a read error. Handle
		// carries dev.ID so a revoke of this device can drop the connection; close on return.
		c := newWSConn(conn)
		_ = server.Handle(ctx, c, dev.ID)
		_ = c.Close()
		srvLog.InfoContext(ctx, "ws disconnect", slog.String("device", dev.ID))
	})
	// A minted join fans out to already-paired devices over the sync hub (DomainJoins), so an
	// admin sees the code live without polling.
	registerPairing(mux, sp.devices, sp.pairings, func() { sp.sync.emitList(appserver.DomainJoins, "") }, log.With(slog.String("component", "pairing")))
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
