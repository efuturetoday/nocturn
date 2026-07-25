package tools_test

import (
	"context"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/tools"
)

// TestPing_GateDeniesUnknownHost is the gate-before-effect guarantee for ping: a denied host never
// opens a socket. We deny and observe gate.ErrDenied rather than any socket/lookup error.
func TestPing_GateDeniesUnknownHost(t *testing.T) {
	ping := toolFrom(t, tools.Config{}, "ping")
	_, err := ping.Call(denyAll(context.Background()), `{"host":"10.255.255.1"}`)
	if err == nil {
		t.Fatal("denied host was pinged")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Fatalf("expected a gate denial before the socket, got %v", err)
	}
}

// TestPing_Egress_ScansHost proves the pinged host is egress-scanned: a stored vault value in the host
// is blocked before any socket or DNS work (after the gate allows).
func TestPing_Egress_ScansHost(t *testing.T) {
	store := secret.NewStore()
	store.Set("api", []byte("SUPERSECRETVALUE123"))
	sc := secret.NewScanner(store)

	ping := toolFrom(t, tools.Config{Scanner: sc}, "ping")
	_, err := ping.Call(allowAll(context.Background()), `{"host":"SUPERSECRETVALUE123.example"}`)
	if err == nil {
		t.Fatal("secret smuggled into the pinged host was not blocked")
	}
	if !strings.Contains(err.Error(), "egress blocked") {
		t.Fatalf("expected an egress block, got %v", err)
	}
}

// TestPing_MissingHost_Rejected proves an empty host is refused before the gate.
func TestPing_MissingHost_Rejected(t *testing.T) {
	ping := toolFrom(t, tools.Config{}, "ping")
	_, err := ping.Call(allowAll(context.Background()), `{"host":""}`)
	if err == nil || !strings.Contains(err.Error(), "missing required field: host") {
		t.Fatalf("empty host not clearly rejected: %v", err)
	}
}

// TestPing_RawIP_NoDNS proves a raw IP is pinged directly (no resolver). It needs an unprivileged ICMP
// socket, which many CI environments forbid — skip cleanly with the tool's own clear socket error.
func TestPing_RawIP_NoDNS(t *testing.T) {
	ping := toolFrom(t, tools.Config{}, "ping")
	out, err := ping.Call(allowAll(context.Background()), `{"host":"127.0.0.1"}`)
	if err != nil {
		if strings.Contains(err.Error(), "ICMP socket") {
			t.Skipf("raw ICMP sockets unavailable here: %v", err)
		}
		t.Fatalf("ping 127.0.0.1: %v", err)
	}
	if !strings.Contains(out, `"ip":"127.0.0.1"`) || !strings.Contains(out, `"ok":true`) {
		t.Fatalf("loopback ping result unexpected: %q", out)
	}
}
