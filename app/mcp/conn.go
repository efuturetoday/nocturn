// conn.go is the gated connection layer for remote MCP servers: it puts this
// package's pure protocol Client behind the SAME security boundary as every
// other outbound effect. The Client is protocol-only (stdlib-only, injected
// Transport); the Conn here supplies the transport — every JSON-RPC POST is an
// http.write on the net host-allowlist (ADR-9), gated through the shared gate on
// tools.NetKind + the server host, with host-owned credential injection (owner
// "mcp:<server>") and the bidirectional leak scan — mirroring app/tools/net.go's
// do(). A remote MCP tool is therefore unreachable without clearing the gate,
// and a connection can only ever reach its own host.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/app/mcp/authflow"
	"github.com/efuturetoday/nocturn/app/secret"
	"github.com/efuturetoday/nocturn/app/tools"
)

// maxResponseBytes caps how much of an MCP response is read (both content
// types) — the same bound as the net tool.
const maxResponseBytes = 10 << 20 // 10 MiB

// AuthRequiredError is returned when a server answers 401 — it wants OAuth (the MCP
// authorization spec). It carries the resource-metadata URL from the WWW-Authenticate
// challenge so a caller can point the operator at `nocturn auth <server>`. The daemon
// treats it as "not connected yet, needs auth", not a hard failure.
type AuthRequiredError struct {
	Server           string
	ResourceMetadata string
}

func (e *AuthRequiredError) Error() string {
	return fmt.Sprintf("mcp: %s needs authorization — run: nocturn auth %s", e.Server, e.Server)
}

// credentialName is the one credential a server config can declare: the
// server's own bearer, injected host-side as "Authorization: Bearer …".
const credentialName = "oauth"

// Owner is the credential-injection owner id for an MCP connection:
// "mcp:<server>". The typed prefix shares ONE owner namespace with plugins
// (plugin.Owner = "plugin:<name>") without colliding — a plugin "github" and
// an MCP server "github" get distinct owners.
func Owner(name string) string { return "mcp:" + name }

// SecretName is the vault key (and binding secret name) for a server's bearer,
// bound to BOTH the server name AND the host it was issued for:
// "mcp:<name>@<host>/oauth". Host-binding is a security boundary: if mcp.json is
// edited to point the SAME-named server at a DIFFERENT host, the key changes, so
// the stored token is not found — the old token is never injected to the new
// host (no silent cross-host exfil). Same host = same key = the token survives
// restarts. The host is lowercased (hostnames are case-insensitive) so the key
// stays stable.
func SecretName(name, host string) string {
	return Owner(name) + "@" + strings.ToLower(host) + "/" + credentialName
}

// Conn is a gated connection to one remote MCP server. Its transport is the ONE
// path any byte takes to the server; every call gates on the net axis for the
// server's own host, so the connection can never reach anywhere else.
type Conn struct {
	server  Server
	creds   *secret.Injector // host-owned credential jar; nil = no injection
	scanner *secret.Scanner  // bidirectional leak scanner; nil = no scanning
	http    *http.Client
	log     *slog.Logger // protocol/transport trace; nil = silent

	host   string
	client *Client
}

// NewConn builds a gated connection to srv. It parses the server host, and — if
// the server declares a credential (OAuth, or a static token) — binds its
// host-owned Bearer under owner "mcp:<name>". The binding only names the secret;
// the value is resolved at injection time (a refreshing OAuth source, or the
// static token the operator seeded in the vault). No I/O happens here; Connect
// performs the handshake. The HTTP client re-gates every redirect hop
// (checkRedirect), so single-host confinement survives a 3xx.
func NewConn(srv Server, creds *secret.Injector, scanner *secret.Scanner) (*Conn, error) {
	u, err := url.Parse(srv.URL)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("mcp: server %q: bad url %q", srv.Name, srv.URL)
	}
	// host carries the port when the URL states one, exactly like net.go's u.Host — so an MCP server
	// and http_read/http_write share ONE grant target (ADR-9). For a normal https URL (implicit 443)
	// u.Host == u.Hostname(), so the common case is identical either way.
	c := &Conn{server: srv, creds: creds, scanner: scanner, log: slog.New(slog.DiscardHandler), host: u.Host}
	c.http = &http.Client{Timeout: 30 * time.Second, CheckRedirect: c.checkRedirect}
	c.client = New(c.transport)
	if (srv.OAuth != nil || srv.Auth == "token") && creds != nil {
		creds.AddBinding(Owner(srv.Name), secret.Binding{
			Secret: SecretName(srv.Name, c.host), Host: c.host,
			Header: "Authorization", Prefix: "Bearer ",
		})
	}
	return c, nil
}

// SetLogger attaches the protocol/transport trace logger (nil ignored), tagging every line with the
// server name. Call at construction, before Connect.
func (c *Conn) SetLogger(l *slog.Logger) {
	if l != nil {
		c.log = l.With("server", c.server.Name)
	}
}

// checkRedirect re-gates AND egress-scans every redirect hop, mirroring
// net.go: a 3xx is a fresh POST to a possibly-different host and must clear the
// same net allowlist. This is what keeps the connection confined to one host.
func (c *Conn) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	ctx := req.Context()
	c.log.Debug("mcp redirect", "to", req.URL.Host, "hop", len(via))
	if err := gate.Check(ctx, gate.Action{Kind: tools.NetKind, Target: req.URL.Host}, tools.HostMatch, tools.NetSuggestions(req.URL.Host)...); err != nil {
		return err
	}
	if c.scanner != nil {
		if err := c.scanner.ScanEgress(req.URL.String(), ""); err != nil {
			return fmt.Errorf("egress blocked: %w", err)
		}
	}
	return nil
}

// transport is the gated HTTP leg under the protocol client, and the SOLE place
// the credential owner enters the request context — every protocol message
// (initialize, tools/list, tools/call) crosses the gate here as an http.write to
// the server's host: gated on tools.NetKind, leak-scanned on egress,
// credential-injected host-side, and redacted on ingress. Mirrors net.go's do().
func (c *Conn) transport(ctx context.Context, body []byte, header http.Header) (*Response, error) {
	// 1. Gate on the net axis: an MCP POST is an http.write on the shared host allowlist, so a host
	// the user already allowed for http_read/http_write covers this server (ADR-9).
	if err := gate.Check(ctx, gate.Action{Kind: tools.NetKind, Target: c.host}, tools.HostMatch, tools.NetSuggestions(c.host)...); err != nil {
		return nil, err
	}
	// 2. Egress scan the caller-built request (URL + JSON-RPC body + protocol header values) BEFORE
	// anything host-owned is added, so the host's own bearer (injected next) is never flagged.
	if c.scanner != nil {
		parts := append([]string{c.server.URL, string(body)}, headerValues(header)...)
		if err := c.scanner.ScanEgress(parts...); err != nil {
			return nil, fmt.Errorf("egress blocked: %w", err)
		}
	}
	// 3. Owner-scope, then inject the host-owned bearer at the border: only THIS connection's
	// credential (or an unowned app default) rides along; the model/script never handles it.
	ctx = secret.WithOwner(ctx, Owner(c.server.Name))
	req := secret.Request{Method: http.MethodPost, URL: c.server.URL, Headers: map[string]string{}}
	for k := range header {
		req.Headers[k] = header.Get(k)
	}
	if c.creds != nil {
		if _, err := c.creds.InjectMatching(ctx, &req, c.host); err != nil {
			return nil, fmt.Errorf("credential injection: %w", err)
		}
	}
	// 4. Build + perform the request.
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	c.log.Debug("mcp request", "host", c.host, "req_bytes", len(body))
	resp, err := c.http.Do(httpReq)
	if err != nil {
		c.log.Warn("mcp request failed", "host", c.host, "err", err)
		return nil, err
	}
	// 5. A non-2xx is a server rejection (distinct from a network failure, where no response arrived).
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		wwwAuth := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		// A 401 means the server wants OAuth (the MCP authorization spec). Surface it as a typed,
		// actionable error — parse the WWW-Authenticate challenge for the resource-metadata URL — so
		// the daemon logs "run nocturn auth <server>" instead of an opaque rejection. The daemon can't
		// open a browser; the interactive flow is `nocturn auth`.
		if resp.StatusCode == http.StatusUnauthorized {
			rm, _ := authflow.ParseWWWAuthenticate(wwwAuth)
			c.log.Warn("mcp server needs authorization", "server", c.server.Name, "resource_metadata", rm)
			return nil, &AuthRequiredError{Server: c.server.Name, ResourceMetadata: rm}
		}
		c.log.Warn("mcp server rejected", "host", c.host, "status", resp.StatusCode)
		return nil, fmt.Errorf("mcp: %s: server rejected the request (HTTP %d)", c.server.Name, resp.StatusCode)
	}
	c.log.Debug("mcp response", "status", resp.StatusCode, "content_type", resp.Header.Get("Content-Type"))
	// 6. Ingress redaction at the boundary, BEFORE protocol parsing — a stored secret echoed back
	// never reaches the model, whichever shape the server chose. Both shapes are line-oriented (one
	// JSON object, or SSE data: frames each on one line), so per-line redaction is loss-free and
	// keeps the stream streaming.
	return &Response{
		ContentType: resp.Header.Get("Content-Type"),
		Header:      resp.Header,
		Body:        newRedactingReader(io.LimitReader(resp.Body, maxResponseBytes), resp.Body, c.scanner),
	}, nil
}

// headerValues flattens an http.Header's values for the egress scan.
func headerValues(h http.Header) []string {
	var out []string
	for _, vs := range h {
		out = append(out, vs...)
	}
	return out
}

// redactingReader redacts stored secrets from a line-oriented response stream
// (secret.Scanner.RedactIngress per line) while preserving the stream shape, so
// the protocol client can parse SSE frames incrementally without this package
// buffering an unbounded stream.
type redactingReader struct {
	src     *bufio.Reader
	closer  io.Closer
	scanner *secret.Scanner
	buf     []byte
	err     error
}

func newRedactingReader(limited io.Reader, closer io.Closer, sc *secret.Scanner) io.ReadCloser {
	return &redactingReader{src: bufio.NewReader(limited), closer: closer, scanner: sc}
}

func (r *redactingReader) Read(p []byte) (int, error) {
	if len(r.buf) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		line, err := r.src.ReadBytes('\n') // returns data AND err at EOF
		r.err = err
		if len(line) > 0 {
			r.buf = r.scanner.RedactIngress(line) // nil scanner = passthrough
		}
		if len(r.buf) == 0 {
			return 0, r.err
		}
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func (r *redactingReader) Close() error { return r.closer.Close() }
