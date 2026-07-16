package netcap_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/netcap"
)

// allowPing permits ping over the given target glob (a read).
func allowPing(targetGlob string) capability.Policy {
	return capability.Policy{Rules: []capability.Rule{
		{Family: "ping", TargetGlob: targetGlob, Writes: capability.MatchRead, Effect: capability.Allow, Epoch: capability.Permanent},
	}}
}

func TestPingTool_ValidatesArguments(t *testing.T) {
	n := &netcap.Net{Guard: &gateway.Guard{Policy: allowPing(capability.Wildcard)}}
	pt, ok := toolByName(n.Tools(), "ping")
	if !ok {
		t.Fatal("Tools() is missing ping")
	}
	if !json.Valid(pt.Parameters) {
		t.Fatalf("ping Parameters is not valid JSON schema: %s", pt.Parameters)
	}
	if _, err := pt.Invoke(context.Background(), `not json`); err == nil {
		t.Fatal("malformed JSON args must error")
	}
	if _, err := pt.Invoke(context.Background(), `{}`); err == nil {
		t.Fatal("missing required host must error")
	}
}

// ping is gated on the host BEFORE any ICMP I/O: an empty policy denies it, so a
// denied probe never touches the network (no privileges, no socket needed to test).
func TestPing_DeniedBeforeIO(t *testing.T) {
	n := &netcap.Net{Guard: &gateway.Guard{Policy: capability.Policy{}}} // deny-by-default
	pt, _ := toolByName(n.Tools(), "ping")
	if _, err := pt.Invoke(context.Background(), `{"host":"example.com"}`); err != gateway.ErrDenied {
		t.Fatalf("denied ping: err = %v, want ErrDenied", err)
	}
}

// A real loopback echo when the OS permits unprivileged ICMP; skipped (not failed)
// where it does not, so CI without ping_group_range stays green.
func TestPing_Loopback(t *testing.T) {
	n := &netcap.Net{Guard: &gateway.Guard{Policy: allowPing(capability.Wildcard)}}
	pt, _ := toolByName(n.Tools(), "ping")

	out, err := pt.Invoke(context.Background(), `{"host":"127.0.0.1"}`)
	if err != nil {
		if strings.Contains(err.Error(), "ICMP socket") || strings.Contains(err.Error(), "forbid") {
			t.Skipf("unprivileged ICMP not available here: %v", err)
		}
		t.Fatalf("ping loopback: %v", err)
	}
	var got netcap.PingResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("ping output %q: %v", out, err)
	}
	if !got.OK || got.IP != "127.0.0.1" {
		t.Errorf("ping result = %+v, want ok to 127.0.0.1", got)
	}
}
