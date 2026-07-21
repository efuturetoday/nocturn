package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
)

// NetAxis is the shared gate Kind every network tool checks, so one host allowlist spans them all.
// The host-matching semantics (a "*.example.com" grant covering subdomains) live in this file, with
// the tool that owns them — the gate stays target-agnostic.
const NetAxis = "net"

const maxBody = 1 << 16 // 64 KiB of body handed back to the model

// Net is the gated HTTP tool. It holds only transport; authority is read from ctx by the gate.
type Net struct {
	client *http.Client
}

// New builds a Net with a bounded HTTP client.
func New() *Net {
	return &Net{client: &http.Client{Timeout: 30 * time.Second}}
}

// Tool exposes http_get: fetch a URL, but only after the gate authorizes its host.
func (n *Net) Tool() (agentkit.Tool, error) {
	return agentkit.NewTool("http_get", "Fetch a URL over HTTP GET and return the response body.",
		n.get,
		agentkit.WithSchema(agentkit.Object(
			agentkit.Prop("url", agentkit.String("the absolute URL to fetch")),
		).Require("url")),
	)
}

func (n *Net) get(ctx context.Context, args string) (string, error) {
	var in struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	u, err := url.Parse(in.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("invalid url: %q", in.URL)
	}

	// Gate the host on the net axis; the tool supplies both the matcher and the widenings a human
	// may pick (the parent-domain wildcard), because only the tool knows what a host pattern means.
	if err := gate.Check(ctx, gate.Action{Kind: NetAxis, Target: u.Host}, hostMatch, suggestions(u.Host)...); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	return fmt.Sprintf("HTTP %d\n\n%s", resp.StatusCode, body), nil
}

// hostMatch reports whether a granted host pattern covers a target host: "*" any, an exact host, or
// a "*.domain" wildcard over that domain and its subdomains.
func hostMatch(pattern, host string) bool {
	switch {
	case pattern == "*" || pattern == host:
		return true
	case strings.HasPrefix(pattern, "*."):
		domain := pattern[2:]
		return host == domain || strings.HasSuffix(host, "."+domain)
	default:
		return false
	}
}

// suggestions offers the human one widening beyond the exact host: allow the whole parent domain.
// "api.example.com" -> a "*.example.com" grant. A bare "example.com" yields no widening.
func suggestions(host string) []gate.Grant {
	if d := parentDomain(host); d != "" {
		return []gate.Grant{{Kind: NetAxis, Target: "*." + d}}
	}
	return nil
}

// parentDomain returns the registrable-ish parent (last two labels) when the host has a subdomain,
// else "". Not a public-suffix parse — a deliberate, simple heuristic the human confirms anyway.
func parentDomain(host string) string {
	labels := strings.Split(host, ".")
	if len(labels) < 3 {
		return ""
	}
	return strings.Join(labels[len(labels)-2:], ".")
}
