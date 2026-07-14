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
	return []brain.Tool{n.readTool(), n.writeTool(), n.resolveTool()}
}

// http.read and http.write are split so the tool the model picks IS the
// authority: reads (safe methods) go through http.read, writes (mutating
// methods) through http.write. The capability name — not the HTTP verb — is what
// the policy gates and what credential bindings scope to.

func (n *Net) readTool() brain.Tool {
	return brain.Tool{
		ToolSpec: brain.ToolSpec{
			Name:        "http.read",
			Description: "Read a URL over HTTP(S) with a safe method (GET/HEAD) and return the response body.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"url":{"type":"string","description":"The URL to read"},` +
				`"method":{"type":"string","enum":["GET","HEAD"],"description":"Safe HTTP method (default GET)"}` +
				`},"required":["url"]}`),
		},
		Invoke: func(ctx context.Context, args string) (string, error) {
			var a struct {
				URL    string `json:"url"`
				Method string `json:"method"`
			}
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if a.URL == "" {
				return "", errors.New("missing required field: url")
			}
			method := methodOrDefault(a.Method, "GET")
			if !isRead(method) {
				return "", fmt.Errorf("http.read supports only GET/HEAD, not %q", a.Method)
			}
			return n.doRequest(ctx, method, a.URL, "", "")
		},
	}
}

func (n *Net) writeTool() brain.Tool {
	return brain.Tool{
		ToolSpec: brain.ToolSpec{
			Name:        "http.write",
			Description: "Send data to a URL with a mutating method (POST/PUT/PATCH/DELETE). This is a write and may require approval.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"url":{"type":"string","description":"The URL to send to"},` +
				`"method":{"type":"string","enum":["POST","PUT","PATCH","DELETE"],"description":"Mutating HTTP method (default POST)"},` +
				`"body":{"type":"string","description":"Request body"},` +
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
			method := methodOrDefault(a.Method, "POST")
			if !isWrite(method) {
				return "", fmt.Errorf("http.write supports only POST/PUT/PATCH/DELETE, not %q", a.Method)
			}
			return n.doRequest(ctx, method, a.URL, a.Body, a.ContentType)
		},
	}
}

// doRequest builds and runs the HTTP request shared by http.read and http.write.
func (n *Net) doRequest(ctx context.Context, method, url, body, contentType string) (string, error) {
	req := secret.Request{Method: method, URL: url}
	if body != "" {
		ct := contentType
		if ct == "" {
			ct = "application/json"
		}
		req.Body = []byte(body)
		req.Headers = map[string]string{"Content-Type": ct}
	}
	resp, err := n.Fetch(ctx, req)
	if err != nil {
		return "", err
	}
	return truncate(string(resp), maxToolResponse), nil
}

func methodOrDefault(m, def string) string {
	if m = strings.ToUpper(strings.TrimSpace(m)); m != "" {
		return m
	}
	return def
}

func isRead(m string) bool  { return m == "GET" || m == "HEAD" }
func isWrite(m string) bool { return m == "POST" || m == "PUT" || m == "PATCH" || m == "DELETE" }

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
