package netcap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// Tools exposes this capability group as brain tools — name, JSON Schema, and an
// Invoke that validates the model's arguments and calls the guarded method. The
// tool contract (schema + argument parsing) lives WITH the capability, not in
// the caller; the caller just collects Tools().
func (n *Net) Tools() []tool.Tool {
	return []tool.Tool{n.readTool(), n.writeTool(), n.resolveTool(), n.pingTool()}
}

// http.read and http.write are split so the tool the model picks IS the
// authority: reads (safe methods) go through http.read, writes (mutating
// methods) through http.write. The capability name — not the HTTP verb — is what
// the policy gates and what credential bindings scope to.

func (n *Net) readTool() tool.Tool {
	return tool.Tool{
		Spec: tool.Spec{
			Name:        "http.read",
			Description: "Read a URL over HTTP(S) with a safe method (GET/HEAD). Returns a JSON object {status, statusText, headers, body} — the response text is in body.",
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

func (n *Net) writeTool() tool.Tool {
	return tool.Tool{
		Spec: tool.Spec{
			Name:        "http.write",
			Description: "Send data to a URL with a mutating method (POST/PUT/PATCH/DELETE). Returns a JSON object {status, statusText, headers, body}. This is a write and may require approval.",
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
	// Return a JSON envelope {status, statusText, headers, body} so the caller —
	// the model, and fetch() in the guest — sees the real outcome, not just the
	// body (a 404 is no longer mistaken for success). Not truncated here: the
	// brain bounds what the model sees, and a script's nocturn.call gets the whole
	// response. The body is already capped at maxResponseBytes (netcap.go).
	out, err := json.Marshal(struct {
		Status     int               `json:"status"`
		StatusText string            `json:"statusText"`
		Headers    map[string]string `json:"headers"`
		Body       string            `json:"body"`
	}{resp.Status, resp.StatusText, resp.Headers, string(resp.Body)})
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func methodOrDefault(m, def string) string {
	if m = strings.ToUpper(strings.TrimSpace(m)); m != "" {
		return m
	}
	return def
}

func isRead(m string) bool  { return m == "GET" || m == "HEAD" }
func isWrite(m string) bool { return m == "POST" || m == "PUT" || m == "PATCH" || m == "DELETE" }

func (n *Net) pingTool() tool.Tool {
	return tool.Tool{
		Spec: tool.Spec{
			Name:        "ping",
			Description: "Send an ICMP echo to a host to check reachability and latency. Returns a JSON object {host, ip, ok, rtt_ms}.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"host":{"type":"string","description":"The hostname or IP to ping"}},"required":["host"]}`),
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
			res, err := n.Ping(ctx, a.Host)
			if err != nil {
				return "", err
			}
			return pingResultJSON(res)
		},
	}
}

func (n *Net) resolveTool() tool.Tool {
	return tool.Tool{
		Spec: tool.Spec{
			Name: "dns.resolve",
			Description: "Resolve a DNS record for a hostname. `type` selects the record: " +
				"A (IPv4, default), AAAA (IPv6), IP (both), MX, TXT, CNAME, NS, PTR (reverse — host is an IP), SRV. " +
				"Returns a JSON object {host, type, records}.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"host":{"type":"string","description":"The hostname to resolve (an IP for PTR)"},` +
				`"type":{"type":"string","enum":["A","AAAA","IP","MX","TXT","CNAME","NS","PTR","SRV"],"description":"DNS record type (default A)"}` +
				`},"required":["host"]}`),
		},
		Invoke: func(ctx context.Context, args string) (string, error) {
			var a struct {
				Host string `json:"host"`
				Type string `json:"type"`
			}
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if a.Host == "" {
				return "", errors.New("missing required field: host")
			}
			records, err := n.Lookup(ctx, a.Host, a.Type)
			if err != nil {
				return "", err
			}
			out, err := json.Marshal(struct {
				Host    string   `json:"host"`
				Type    string   `json:"type"`
				Records []string `json:"records"`
			}{a.Host, normalizeRecordType(a.Type), records})
			if err != nil {
				return "", err
			}
			return string(out), nil
		},
	}
}
