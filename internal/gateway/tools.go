package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// maxToolResponse caps how much of a fetched body is handed to the model, to
// keep the context bounded.
const maxToolResponse = 4000

// Tools exposes this capability group as brain tools — name, JSON Schema, and an
// Invoke that validates the model's arguments and calls the guarded method. The
// tool contract (schema + argument parsing) lives WITH the capability, not in
// the caller; the caller just collects Tools().
func (n *Net) Tools() []brain.Tool {
	return []brain.Tool{n.fetchTool(), n.resolveTool()}
}

func (n *Net) fetchTool() brain.Tool {
	return brain.Tool{
		ToolSpec: brain.ToolSpec{
			Name:        "net.fetch",
			Description: "Fetch the contents of a URL over HTTP(S).",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"The URL to fetch"}},"required":["url"]}`),
		},
		Invoke: func(ctx context.Context, args string) (string, error) {
			var a struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if a.URL == "" {
				return "", errors.New("missing required field: url")
			}
			body, err := n.Fetch(ctx, secret.Request{URL: a.URL})
			if err != nil {
				return "", err
			}
			return truncate(string(body), maxToolResponse), nil
		},
	}
}

func (n *Net) resolveTool() brain.Tool {
	return brain.Tool{
		ToolSpec: brain.ToolSpec{
			Name:        "dns.resolve",
			Description: "Resolve a hostname to its IP addresses.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"host":{"type":"string","description":"The hostname to resolve"}},"required":["host"]}`),
		},
		Invoke: func(ctx context.Context, args string) (string, error) {
			var a struct {
				Host string `json:"host"`
			}
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if a.Host == "" {
				return "", errors.New("missing required field: host")
			}
			addrs, err := n.Resolve(ctx, a.Host)
			if err != nil {
				return "", err
			}
			return strings.Join(addrs, ", "), nil
		},
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…(truncated)"
	}
	return s
}
