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
			Description: "Make an HTTP(S) request to a URL and return the response body. Defaults to GET; set method + body to send data (POST/PUT/PATCH/DELETE).",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"url":{"type":"string","description":"The URL to request"},` +
				`"method":{"type":"string","enum":["GET","HEAD","POST","PUT","PATCH","DELETE"],"description":"HTTP method (default GET)"},` +
				`"body":{"type":"string","description":"Request body, for POST/PUT/PATCH"},` +
				`"content_type":{"type":"string","description":"Content-Type of the body (default application/json)"}` +
				`},"required":["url"]}`),
		},
		Invoke: func(ctx context.Context, args string) (string, error) {
			var a struct {
				URL         string `json:"url"`
				Method      string `json:"method"`
				Body        string `json:"body"`
				ContentType string `json:"content_type"`
			}
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if a.URL == "" {
				return "", errors.New("missing required field: url")
			}
			method := strings.ToUpper(strings.TrimSpace(a.Method))
			if method == "" {
				method = "GET"
			}
			if !validMethod(method) {
				return "", fmt.Errorf("unsupported method %q", a.Method)
			}

			req := secret.Request{Method: method, URL: a.URL}
			if a.Body != "" {
				ct := a.ContentType
				if ct == "" {
					ct = "application/json"
				}
				req.Body = []byte(a.Body)
				req.Headers = map[string]string{"Content-Type": ct}
			}

			body, err := n.Fetch(ctx, req)
			if err != nil {
				return "", err
			}
			return truncate(string(body), maxToolResponse), nil
		},
	}
}

// validMethod reports whether m is an HTTP method net.fetch will send.
func validMethod(m string) bool {
	switch m {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE":
		return true
	}
	return false
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
