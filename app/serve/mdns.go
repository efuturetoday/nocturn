package serve

import (
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/libp2p/zeroconf/v2"
)

// mdnsService is the DNS-SD service type the companion app browses for.
const mdnsService = "_nocturn._tcp"

// advertiseMDNS announces the daemon on the LAN as _nocturn._tcp (Bonjour/DNS-SD) on the listen
// port, so the app discovers it natively without a typed IP. A TXT record carries the WebSocket
// path. It returns a shutdown func to stop advertising. Register-only — the app browses with its own
// native mDNS. Meaningless on a loopback-only bind (nothing off-box can reach it), so the caller
// skips it there.
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

	server, err := zeroconf.Register(instance, mdnsService, "local.", port, []string{"path=/ws"}, nil)
	if err != nil {
		return nil, err
	}
	return server.Shutdown, nil
}

// isLoopbackAddr reports whether a listen address is loopback-only. An empty host (":8765" = all
// interfaces) is NOT loopback — the daemon is reachable off-box, so advertising is meaningful.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	switch host {
	case "":
		return false // all interfaces
	case "localhost":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
