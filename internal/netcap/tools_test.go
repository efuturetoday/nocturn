package netcap_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/netcap"
	"github.com/efuturetoday/nocturn/internal/tool"
)

func toolByName(tools []tool.Tool, name string) (tool.Tool, bool) {
	for _, t := range tools {
		if t.Name == name {
			return t, true
		}
	}
	return tool.Tool{}, false
}

// The capability group exports its own tools with valid JSON-Schema args — the
// tool contract lives with the capability, not in the caller.
func TestTools_ExposesCapabilitiesWithSchemas(t *testing.T) {
	n := &netcap.Net{Guard: &gateway.Guard{Policy: capability.Policy{}}}
	tools := n.Tools()

	for _, name := range []string{"http.read", "http.write", "dns.resolve"} {
		tl, ok := toolByName(tools, name)
		if !ok {
			t.Fatalf("Tools() is missing %q", name)
		}
		if !json.Valid(tl.Parameters) {
			t.Fatalf("%q Parameters is not valid JSON schema: %s", name, tl.Parameters)
		}
		if tl.Invoke == nil {
			t.Fatalf("%q has no Invoke", name)
		}
	}
}

func TestFetchTool_ValidatesArguments(t *testing.T) {
	n := &netcap.Net{Guard: &gateway.Guard{Policy: allowRead(capability.Wildcard)}}
	ft, _ := toolByName(n.Tools(), "http.read")

	if _, err := ft.Invoke(context.Background(), `not json`); err == nil {
		t.Fatal("malformed JSON args must error")
	}
	if _, err := ft.Invoke(context.Background(), `{"nope":"x"}`); err == nil {
		t.Fatal("missing required url must error")
	}
}

func TestFetchTool_Invoke_ReturnsEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("pong"))
	}))
	defer srv.Close()

	n := &netcap.Net{Guard: &gateway.Guard{Policy: allowRead(capability.Wildcard)}}
	ft, _ := toolByName(n.Tools(), "http.read")

	out, err := ft.Invoke(context.Background(), fmt.Sprintf(`{"url":%q}`, srv.URL))
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	var env struct {
		Status     int               `json:"status"`
		StatusText string            `json:"statusText"`
		Headers    map[string]string `json:"headers"`
		Body       string            `json:"body"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope %q: %v", out, err)
	}
	if env.Body != "pong" || env.Status != 200 {
		t.Fatalf("envelope = %+v, want body=pong status=200", env)
	}
	if !strings.Contains(env.Headers["Content-Type"], "text/plain") {
		t.Fatalf("headers = %v, want Content-Type text/plain", env.Headers)
	}
}
