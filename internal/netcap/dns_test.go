package netcap_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/netcap"
)

func allowResolve(hostGlob string) capability.Policy {
	return capability.Policy{Rules: []capability.Rule{
		{Capability: "dns.resolve", HostGlob: hostGlob, Effect: capability.Allow, Epoch: capability.Permanent},
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
		{Capability: "dns.resolve", HostGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
	}}
	n := &netcap.Net{Guard: &gateway.Guard{Policy: policy, Approvals: askEngine(false), TTL: time.Second}}
	if _, err := n.Resolve(context.Background(), "localhost"); !errors.Is(err, gateway.ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
}
