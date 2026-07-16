package mcpcap

import (
	"testing"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
)

// New fixes the connection's ceiling to exactly the server's host: one http reach
// there (read+write), nothing anywhere else — a connection can never be talked into
// reaching a different host or family, regardless of base policy. (The transport
// stamps this ceiling onto every call's ctx; that path is exercised end-to-end by
// the external tests.)
func TestNew_CeilingBoundsToServerHost(t *testing.T) {
	conn, err := New(Server{Name: "test", URL: "https://mcp.example.com/mcp"}, &gateway.Guard{}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	allowed := []capability.Call{
		{Family: "http", Mutates: true, Target: "mcp.example.com"},
		{Family: "http", Mutates: false, Target: "mcp.example.com"},
	}
	denied := []capability.Call{
		{Family: "http", Mutates: true, Target: "evil.example.com"},
		{Family: "dns", Mutates: false, Target: "mcp.example.com"},
		{Family: "file", Mutates: true, Target: "/work/x"},
	}
	for _, c := range allowed {
		if !conn.ceiling.Allows(c) {
			t.Errorf("ceiling denies %+v, want allowed", c)
		}
	}
	for _, c := range denied {
		if conn.ceiling.Allows(c) {
			t.Errorf("ceiling allows %+v, want denied", c)
		}
	}
}

func TestNew_RejectsBadURL(t *testing.T) {
	for _, u := range []string{"", "not a url", "https://"} {
		if _, err := New(Server{Name: "x", URL: u}, &gateway.Guard{}, nil, nil, nil); err == nil {
			t.Errorf("New accepted url %q", u)
		}
	}
}
