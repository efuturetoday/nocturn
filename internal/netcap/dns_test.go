package netcap_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/netcap"
)

func allowResolve(hostGlob string) capability.Policy {
	return capability.Policy{Rules: []capability.Rule{
		{Family: "dns", TargetGlob: hostGlob, Writes: capability.MatchRead, Effect: capability.Allow, Epoch: capability.Permanent},
	}}
}

func TestResolve_Allow_ReturnsAddrs(t *testing.T) {
	n := &netcap.Net{Guard: &gateway.Guard{Policy: allowResolve(capability.Wildcard)}}
	addrs, err := n.Resolve(context.Background(), "localhost")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(addrs) == 0 {
		t.Fatal("expected at least one address for localhost")
	}
}

// The record type selects the family: A yields only IPv4, AAAA only IPv6, IP both.
// localhost resolves to 127.0.0.1 and ::1, so the split is observable without a
// network. An unsupported type is a clear error.
func TestLookup_RecordType(t *testing.T) {
	n := &netcap.Net{Guard: &gateway.Guard{Policy: allowResolve(capability.Wildcard)}}
	isV4 := func(s string) bool { ip := net.ParseIP(s); return ip != nil && ip.To4() != nil }

	a, err := n.Lookup(context.Background(), "localhost", "A")
	if err != nil {
		t.Fatalf("A: %v", err)
	}
	for _, s := range a {
		if !isV4(s) {
			t.Errorf("A returned non-IPv4 %q", s)
		}
	}

	aaaa, err := n.Lookup(context.Background(), "localhost", "AAAA")
	if err != nil {
		t.Fatalf("AAAA: %v", err)
	}
	for _, s := range aaaa {
		if isV4(s) {
			t.Errorf("AAAA returned IPv4 %q", s)
		}
	}

	// An empty type defaults to A.
	if def, err := n.Lookup(context.Background(), "localhost", ""); err != nil || len(def) == 0 {
		t.Fatalf("default type: %v (records=%v)", err, def)
	}

	if _, err := n.Lookup(context.Background(), "localhost", "BOGUS"); err == nil {
		t.Fatal("unsupported record type must error")
	}
}

func TestResolve_Deny(t *testing.T) {
	n := &netcap.Net{Guard: &gateway.Guard{Policy: capability.Policy{}}} // deny-by-default
	if _, err := n.Resolve(context.Background(), "localhost"); !errors.Is(err, gateway.ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
}

func TestResolve_HostAllowlist_DeniesOtherHost(t *testing.T) {
	n := &netcap.Net{Guard: &gateway.Guard{Policy: allowResolve("*.example.com")}}
	if _, err := n.Resolve(context.Background(), "localhost"); !errors.Is(err, gateway.ErrDenied) {
		t.Fatalf("resolve of non-allowlisted host: err = %v, want ErrDenied", err)
	}
}

func TestResolve_Ask_DenyBlocks(t *testing.T) {
	policy := capability.Policy{Rules: []capability.Rule{
		{Family: "dns", TargetGlob: capability.Wildcard, Writes: capability.MatchRead, Effect: capability.Ask, Epoch: capability.Permanent},
	}}
	n := &netcap.Net{Guard: &gateway.Guard{Policy: policy, Approvals: askEngine(false), TTL: time.Second}}
	if _, err := n.Resolve(context.Background(), "localhost"); !errors.Is(err, gateway.ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
}
