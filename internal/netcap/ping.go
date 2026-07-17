package netcap

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
)

// pingTimeout bounds how long a single echo waits for its reply.
const pingTimeout = 4 * time.Second

// PingResult is the outcome of a gated reachability probe.
type PingResult struct {
	Host  string `json:"host"`
	IP    string `json:"ip"`
	OK    bool   `json:"ok"`
	RTTms int64  `json:"rtt_ms"`
}

// Ping sends one ICMP echo to host and reports whether it answered, and how fast.
// Like Resolve it is a sibling networking capability on the shared Guard: ICMP is
// an exfiltration channel (the destination host — and the payload — carry data to
// an attacker's box), so it is gated on the host exactly as dns.resolve is, and an
// unknown host escalates to human approval. A read (Write:false): a probe observes,
// it does not mutate.
//
// The socket is opened unprivileged (a UDP-based ICMP socket, "udp4"/"udp6"), which
// needs no root on macOS and on Linux where net.ipv4.ping_group_range permits it —
// a clear error is returned when the OS forbids it rather than a silent failure.
func (n *Net) Ping(ctx context.Context, host string) (*PingResult, error) {
	call := capability.Call{Family: "ping", Write: false, Target: host}
	intent := "ping " + host
	// Egress: the ping target is the exfiltration surface, same channel as a DNS name.
	return gateway.Do(ctx, n.Guard, call, intent,
		gateway.ScanEgress(n.Scanner, func() []string { return []string{host} }),
		func() (*PingResult, error) {
			// A raw IP (v4 or v6) is pinged directly — no DNS at all, so `ping 127.0.0.1`
			// or `ping ::1` never depends on the resolver. Only a hostname is resolved.
			var ips []net.IP
			if ip := net.ParseIP(host); ip != nil {
				ips = []net.IP{ip}
			} else {
				resolver := n.Resolver
				if resolver == nil {
					resolver = net.DefaultResolver
				}
				resolved, err := resolver.LookupIP(ctx, "ip", host)
				if err != nil {
					return nil, err
				}
				ips = resolved
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("netcap: no address for %q", host)
			}
			// LookupIP returns A and AAAA in an unspecified order, and a machine may have
			// only one family reachable. Try each resolved address (echo picks v4/v6 by the
			// address family) until one answers, so a v6-first result on a v4-only host — or
			// a genuinely v6-only host — still pings. The last error is reported if none do.
			var lastErr error
			for _, dst := range ips {
				rtt, err := echo(ctx, dst)
				if err != nil {
					lastErr = err
					continue
				}
				return &PingResult{Host: host, IP: dst.String(), OK: true, RTTms: rtt.Milliseconds()}, nil
			}
			return nil, lastErr
		})
}

// echo sends a single ICMP echo request to dst and returns the round-trip time,
// choosing IPv4 or IPv6 by the address family.
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
		return 0, fmt.Errorf("netcap: ping needs an ICMP socket (%w); the OS may forbid unprivileged ping", err)
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
			return 0, fmt.Errorf("netcap: ping to %s timed out or failed: %w", dst, err)
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

// pingResultJSON marshals a PingResult for a tool caller.
func pingResultJSON(r *PingResult) (string, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
