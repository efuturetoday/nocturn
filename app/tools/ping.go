package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
)

// pingTimeout bounds how long a single ICMP echo waits for its reply.
const pingTimeout = 4 * time.Second

// ping gates on NetKind with the target host, like dns_resolve and fetch: ICMP is an exfiltration
// channel (the pinged host, and the payload, carry data off-box), so an unknown host escalates to the
// same human approval. It is a read: a probe observes, it never mutates.

// pingResult is the outcome of a gated reachability probe.
type pingResult struct {
	Host  string `json:"host"`
	IP    string `json:"ip"`
	OK    bool   `json:"ok"`
	RTTms int64  `json:"rtt_ms"`
}

func (n *Net) pingTool() (agentkit.Tool, error) {
	return agentkit.NewTool("ping",
		"Send one ICMP echo to a host and report whether it answered. Returns a JSON object {host, ip, ok, rtt_ms}.",
		n.ping,
		agentkit.WithSchema(agentkit.Object(
			agentkit.Prop("host", agentkit.String("The hostname or IP to ping")),
		).Require("host")),
	)
}

func (n *Net) ping(ctx context.Context, args string) (string, error) {
	var a struct {
		Host string `json:"host"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Host == "" {
		return "", errors.New("missing required field: host")
	}
	if err := gate.Check(ctx, gate.Action{Kind: NetKind, Target: a.Host}, hostMatch, suggestions(a.Host)...); err != nil {
		return "", err
	}
	if n.scanner != nil {
		if err := n.scanner.ScanEgress(a.Host, ""); err != nil {
			return "", fmt.Errorf("egress blocked: %w", err)
		}
	}

	// A raw IP (v4 or v6) is pinged directly — no DNS — so `ping 127.0.0.1` never depends on the
	// resolver. Only a hostname is resolved.
	var ips []net.IP
	if ip := net.ParseIP(a.Host); ip != nil {
		ips = []net.IP{ip}
	} else {
		resolved, err := net.DefaultResolver.LookupIP(ctx, "ip", a.Host)
		if err != nil {
			return "", err
		}
		ips = resolved
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("no address for %q", a.Host)
	}
	// LookupIP returns A and AAAA in an unspecified order; try each until one answers (echo picks
	// v4/v6 by address family), so a v6-first result on a v4-only host still pings.
	var lastErr error
	for _, dst := range ips {
		rtt, err := echo(ctx, dst)
		if err != nil {
			lastErr = err
			continue
		}
		return jsonResult(pingResult{Host: a.Host, IP: dst.String(), OK: true, RTTms: rtt.Milliseconds()})
	}
	return "", lastErr
}

// echo sends a single ICMP echo request to dst and returns the round-trip time, choosing IPv4 or
// IPv6 by the address family. The socket is opened unprivileged (a UDP-based ICMP socket), which
// needs no root on macOS and on Linux where net.ipv4.ping_group_range permits it.
func echo(ctx context.Context, dst net.IP) (time.Duration, error) {
	var (
		network  string
		proto    int
		msgType  icmp.Type
		wantType icmp.Type
	)
	if dst.To4() != nil {
		network, proto, msgType, wantType = "udp4", 1, ipv4.ICMPTypeEcho, ipv4.ICMPTypeEchoReply
	} else {
		network, proto, msgType, wantType = "udp6", 58, ipv6.ICMPTypeEchoRequest, ipv6.ICMPTypeEchoReply
	}

	conn, err := icmp.ListenPacket(network, "")
	if err != nil {
		return 0, fmt.Errorf("ping needs an ICMP socket (%w); the OS may forbid unprivileged ping", err)
	}
	defer conn.Close()

	msg := icmp.Message{
		Type: msgType, Code: 0,
		Body: &icmp.Echo{ID: os.Getpid() & 0xffff, Seq: 1, Data: []byte("nocturn")},
	}
	wire, err := msg.Marshal(nil)
	if err != nil {
		return 0, err
	}

	deadline := time.Now().Add(pingTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return 0, err
	}

	start := time.Now()
	// For a UDP-based ICMP socket the kernel wants an address with the port unset.
	if _, err := conn.WriteTo(wire, &net.UDPAddr{IP: dst}); err != nil {
		return 0, err
	}

	reply := make([]byte, 1500)
	for {
		nRead, _, err := conn.ReadFrom(reply)
		if err != nil {
			return 0, fmt.Errorf("ping to %s timed out or failed: %w", dst, err)
		}
		parsed, err := icmp.ParseMessage(proto, reply[:nRead])
		if err != nil {
			continue // not a message we can read; keep waiting until the deadline
		}
		if parsed.Type == wantType {
			return time.Since(start), nil
		}
	}
}
