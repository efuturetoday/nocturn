// Package tools provides ready-made effect tools for agentkit that gate their own effect through
// agentkit-gate. They are the "base capabilities as tools": a tool that reaches the network gates
// the target HOST itself before the request, so the permission layer sees a normalized target
// without knowing the tool's argument format.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit-gate"
)

// NetAxis is the shared gate axis for every network tool. Gating on this one name (instead of each
// tool's own name) means a single host allowlist — and a single approval — covers http_get, a future
// http_post, dns, ping, … alike.
const NetAxis = "net"

// HTTPGet returns a tool that fetches a URL over HTTP GET. Before the request it gates the target
// host on NetAxis via gate.Check, so a new host prompts the human and an approved host is remembered
// (and shared across all network tools). Ungated when no gate machinery is installed.
func HTTPGet(opts ...HTTPOption) agentkit.Tool {
	g := httpGet{client: http.DefaultClient, limit: 4000}
	for _, o := range opts {
		o(&g)
	}
	return g
}

// HTTPOption configures HTTPGet.
type HTTPOption func(*httpGet)

// WithClient sets the http.Client (default: http.DefaultClient).
func WithClient(client *http.Client) HTTPOption { return func(g *httpGet) { g.client = client } }

// WithLimit caps how many response bytes are read (default 4000).
func WithLimit(n int) HTTPOption { return func(g *httpGet) { g.limit = int64(n) } }

type httpGet struct {
	client *http.Client
	limit  int64
}

func (httpGet) Spec() agentkit.ToolSpec {
	return agentkit.ToolSpec{
		Name:        "http_get",
		Description: "Fetch a URL over HTTP GET; returns the status and the (truncated) response body.",
		Parameters:  agentkit.Object(agentkit.Prop("url", agentkit.String("the URL to fetch"))).Require("url"),
	}
}

func (g httpGet) Call(ctx context.Context, args string) (string, error) {
	var in struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "", fmt.Errorf("http_get: invalid arguments: %w", err)
	}
	u, err := url.Parse(in.URL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("http_get: invalid url %q", in.URL)
	}

	// Host axis: gate the target on NetAxis before the request, with the host matcher and the
	// widening the human may pick (*.domain). A denial is surfaced to the model.
	if err := gate.Check(ctx, gate.Action{Kind: NetAxis, Target: u.Host}, HostMatch, HostSuggestions(u.Host)...); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.URL, nil)
	if err != nil {
		return "", fmt.Errorf("http_get: %w", err)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http_get: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, g.limit))
	if err != nil {
		return "", fmt.Errorf("http_get: read body: %w", err)
	}
	return fmt.Sprintf("%s\n%s", resp.Status, body), nil
}

var _ agentkit.Tool = httpGet{}
