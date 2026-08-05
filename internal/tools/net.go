package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// NetKind is the shared gate Kind every network tool checks, so one host allowlist spans them all.
// The host-matching semantics (a "*.example.com" grant covering subdomains) live in this file, with
// the tool that owns them — the gate stays target-agnostic.
const NetKind = "net"

const maxBody = 1 << 16 // 64 KiB of response body handed back to the caller

// Net is the gated HTTP tool group. It holds only transport plus a host-owned credential jar and a
// bidirectional leak scanner; authority is read from ctx by the gate. A bearer is injected host-side
// so the model (and any script/plugin behind it) never sees it, and the scanner blocks a secret the
// caller tries to smuggle OUT and redacts one echoed back IN.
type Net struct {
	client  *http.Client
	secrets *secret.Injector // host-owned, host-bound credential jar; nil = no injection
	scanner *secret.Scanner  // bidirectional secret leak scanner; nil = no scanning
	log     *slog.Logger     // effect trace; nil = silent (gate/scanner log deny/egress themselves)
}

// New builds a Net with a bounded HTTP client, an optional credential injector and an optional leak
// scanner (nil = that feature off). The client re-gates every redirect hop (see checkRedirect): a 3xx
// is a fresh request to a possibly-different host, so it must clear the same host allowlist and egress
// scan as the original — otherwise a redirect is a gate/exfil bypass.
func New(secrets *secret.Injector, scanner *secret.Scanner) *Net {
	n := &Net{secrets: secrets, scanner: scanner, log: slog.New(slog.DiscardHandler)}
	n.client = &http.Client{Timeout: 30 * time.Second, CheckRedirect: n.checkRedirect}
	return n
}

// SetLogger attaches the effect-trace logger (nil ignored). Call at construction, before the Net is
// shared across tool calls.
func (n *Net) SetLogger(l *slog.Logger) {
	if l != nil {
		n.log = l
	}
}

// checkRedirect authorizes each redirect the client is about to follow. The redirected request carries
// the original request's context (so the gate machinery is still installed), and req.URL is the NEW
// target — we gate and egress-scan it exactly like the first hop. Returning an error stops the follow
// and surfaces as the do() "fetch" error, so the redirect's body never reaches the caller.
func (n *Net) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	ctx := req.Context()
	if req.URL.User != nil {
		return errors.New("redirect to a url carrying credentials")
	}
	if err := gate.Check(ctx, gate.Action{Kind: NetKind, Target: req.URL.Host}, HostMatch, NetSuggestions(req.URL.Host)...); err != nil {
		return err
	}
	if n.scanner != nil {
		if err := n.scanner.ScanEgress(req.URL.String(), ""); err != nil {
			return fmt.Errorf("egress blocked: %w", err)
		}
	}
	return nil
}

// Tools exposes the network tools. http_read and http_write are split so the tool the model (or a
// guest's fetch) picks IS the intent: safe methods go through http_read, mutating ones through
// http_write. Both return a JSON envelope {status, statusText, headers, body} so a caller sees the
// real outcome, not just a body (a 404 is not mistaken for success) — the same shape the prelude's
// fetch() reconstructs into a Response.
func (n *Net) Tools() ([]agentkit.Tool, error) {
	read, err := agentkit.NewTool("http_read",
		"Read a URL over HTTP(S) with a safe method (GET/HEAD). Returns a JSON object {status, statusText, headers, body} — the response text is in body.",
		n.read,
		agentkit.WithSchema(agentkit.Object(
			agentkit.Prop("url", agentkit.String("The URL to read")),
			agentkit.Prop("method", agentkit.String("Safe HTTP method (default GET)").WithEnum("GET", "HEAD")),
		).Require("url")),
	)
	if err != nil {
		return nil, err
	}
	write, err := agentkit.NewTool("http_write",
		"Send data to a URL with a mutating method (POST/PUT/PATCH/DELETE). Returns a JSON object {status, statusText, headers, body}. This is a write and may require approval.",
		n.write,
		agentkit.WithSchema(agentkit.Object(
			agentkit.Prop("url", agentkit.String("The URL to send to")),
			agentkit.Prop("method", agentkit.String("Mutating HTTP method (default POST)").WithEnum("POST", "PUT", "PATCH", "DELETE")),
			agentkit.Prop("body", agentkit.String("Request body")),
			agentkit.Prop("content_type", agentkit.String("Content-Type of the body (default application/json)")),
		).Require("url")),
	)
	if err != nil {
		return nil, err
	}
	resolve, err := n.resolveTool()
	if err != nil {
		return nil, err
	}
	ping, err := n.pingTool()
	if err != nil {
		return nil, err
	}
	return []agentkit.Tool{read, write, resolve, ping}, nil
}

func (n *Net) read(ctx context.Context, args string) (string, error) {
	var a struct {
		URL    string `json:"url"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	method := methodOrDefault(a.Method, http.MethodGet)
	if method != http.MethodGet && method != http.MethodHead {
		return "", fmt.Errorf("http_read supports only GET/HEAD, not %q", a.Method)
	}
	return n.do(ctx, method, a.URL, "", "")
}

func (n *Net) write(ctx context.Context, args string) (string, error) {
	var a struct {
		URL         string `json:"url"`
		Method      string `json:"method"`
		Body        string `json:"body"`
		ContentType string `json:"content_type"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	method := methodOrDefault(a.Method, http.MethodPost)
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return "", fmt.Errorf("http_write supports only POST/PUT/PATCH/DELETE, not %q", a.Method)
	}
	return n.do(ctx, method, a.URL, a.Body, a.ContentType)
}

// do is the shared request path for both tools: validate + gate the host, leak-scan the outbound
// request, inject any host-owned credential at the border, perform the request, redact the response,
// and return the JSON envelope.
func (n *Net) do(ctx context.Context, method, rawURL, body, contentType string) (string, error) {
	if rawURL == "" {
		return "", errors.New("missing required field: url")
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("invalid url: %q", rawURL)
	}
	if u.User != nil {
		return "", errors.New("invalid url: credentials in the url are not accepted")
	}

	// Gate the host on the net kind; the tool supplies both the matcher and the widenings a human may
	// pick (the parent-domain wildcard), because only the tool knows what a host pattern means.
	if err := gate.Check(ctx, gate.Action{Kind: NetKind, Target: u.Host}, HostMatch, NetSuggestions(u.Host)...); err != nil {
		return "", err
	}

	// Egress leak scan the CALLER-built request (url + body) before anything host-owned is added — a
	// secret smuggled into the path/query/body is blocked here. Runs before injection so the host's
	// own bearer (added next) is never mistaken for a leak.
	if n.scanner != nil {
		if err := n.scanner.ScanEgress(u.String(), body); err != nil {
			return "", fmt.Errorf("egress blocked: %w", err)
		}
	}

	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bodyReader)
	if err != nil {
		return "", err
	}
	if body != "" {
		ct := contentType
		if ct == "" {
			ct = "application/json"
		}
		req.Header.Set("Content-Type", ct)
	}

	// Inject any host-owned credential bound to this host, at the border: the value comes from the
	// vault and is scoped to the caller (WithOwner in ctx), so the model/script never handles it. A
	// locked vault or no matching binding injects nothing — the request just goes out unauthenticated.
	if n.secrets != nil {
		sr := secret.Request{Method: method, URL: u.String(), Headers: map[string]string{}}
		if _, err := n.secrets.InjectMatching(ctx, &sr, u.Host); err != nil {
			return "", fmt.Errorf("credential injection: %w", err)
		}
		for k, v := range sr.Headers {
			req.Header.Set(k, v)
		}
	}

	resp, err := n.client.Do(req)
	if err != nil {
		n.log.Warn("http request failed", "method", method, "host", u.Host, "err", err)
		return "", fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	n.log.Debug("http request", "method", method, "host", u.Host, "status", resp.StatusCode)

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	// Ingress redaction: strip any known secret echoed back before the response reaches the caller, so
	// a reflecting endpoint can't launder a vault value into the transcript.
	if n.scanner != nil {
		respBody = n.scanner.RedactIngress(respBody)
	}

	out, err := json.Marshal(struct {
		Status     int               `json:"status"`
		StatusText string            `json:"statusText"`
		Headers    map[string]string `json:"headers"`
		Body       string            `json:"body"`
	}{resp.StatusCode, resp.Status, n.safeHeaders(resp.Header), string(respBody)})
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

// credentialHeaders are response headers dropped wholesale before the envelope reaches the caller:
// they carry credentials or auth challenges a reflecting/authenticating endpoint could otherwise
// launder into the model transcript. Canonical (http.Header) casing; lookup is exact after
// CanonicalMIMEHeaderKey normalises the incoming name.
var credentialHeaders = map[string]bool{
	"Set-Cookie":          true,
	"Set-Cookie2":         true,
	"Authorization":       true,
	"Proxy-Authorization": true,
	"Www-Authenticate":    true,
	"Proxy-Authenticate":  true,
}

// safeHeaders flattens http.Header (map[string][]string) to one value per name — the first — after
// dropping credential-bearing headers entirely and ingress-redacting any known vault value echoed in
// the surviving values. Without this the header map is a leak channel the body scanner never sees:
// Set-Cookie / WWW-Authenticate reach the model raw, and a secret reflected into an arbitrary header
// slips past RedactIngress (which scans only the body). Almost every header is single-valued; a
// genuinely multi-valued header keeps only its first value here.
func (n *Net) safeHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if credentialHeaders[http.CanonicalHeaderKey(k)] || len(v) == 0 {
			continue
		}
		val := v[0]
		if n.scanner != nil {
			val = string(n.scanner.RedactIngress([]byte(val)))
		}
		out[k] = val
	}
	return out
}

// HostMatch reports whether a granted host pattern covers a target host: "*" any, an exact host, or
// a "*.domain" wildcard over that domain and its subdomains. Exported so other net-axis tools (e.g.
// the gated MCP transport) gate on the SAME allowlist with the SAME target semantics.
func HostMatch(pattern, host string) bool {
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

// NetSuggestions offers the human one widening beyond the exact host: allow the whole parent domain.
// "api.example.com" -> a "*.example.com" grant. A bare "example.com" yields no widening. Exported
// alongside HostMatch so the MCP transport offers the same widening on the net axis.
func NetSuggestions(host string) []gate.Grant {
	if d := parentDomain(host); d != "" {
		return []gate.Grant{{Kind: NetKind, Target: "*." + d}}
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
