// Package mcpcap is the gated connection layer for remote MCP servers: the
// interface-adapter that puts internal/mcp's protocol client behind the SAME
// security boundary as every other outbound effect. The protocol client is pure
// (stdlib-only, injected Transport); THIS package supplies the transport: every
// JSON-RPC POST is an http.write to the server's host, run through
// gateway.Guard (broker + out-of-band HITL), with host-owned credential
// injection (owner "mcp:<server>") and the bidirectional leak scan — mirroring
// netcap. A remote MCP tool is therefore unreachable without passing the
// broker, and the connection's ceiling bounds it to its own declared host.
package mcpcap

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/mcp"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// maxResponseBytes caps how much of an MCP response is read (both content
// types) — the same bound as netcap.
const maxResponseBytes = 10 << 20 // 10 MiB

// CredentialName is the one credential a server config can declare: the
// server's own bearer, injected host-side as "Authorization: Bearer …".
const CredentialName = "oauth"

// Owner is the credential-injection owner id for an MCP connection:
// "mcp:<server>". The typed prefix shares ONE owner namespace with plugins
// (plugin.Owner = "plugin:<name>") without colliding — a plugin "github" and
// an MCP server "github" get distinct owners.
func Owner(name string) string { return "mcp:" + name }

// SecretName is the vault key (and binding secret name) for a server's bearer,
// bound to BOTH the server name AND the host it was issued for:
// "mcp:<name>@<host>/oauth". Host-binding is a security boundary: if mcp.json is
// edited to point the SAME-named server at a DIFFERENT host, the key changes, so
// the stored token is not found — the operator is re-prompted and the old token
// is never injected to the new host (no silent cross-host exfil). Same host =
// same key = the token survives restarts as before. The host is lowercased
// (hostnames are case-insensitive) so the key stays stable.
func SecretName(name, host string) string {
	return Owner(name) + "@" + strings.ToLower(host) + "/" + CredentialName
}

// StatusError is a non-2xx HTTP response from an MCP server: the server WAS
// reached and rejected the request (unlike a network error, where no response
// arrived). It carries the status so the setup layer can offer to fix the
// credential — a rejected request is a plausible sign of a bad/expired/revoked
// token — without touching anything on a mere connectivity failure.
type StatusError struct {
	Server string
	Status int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("mcpcap: %s: HTTP %d", e.Server, e.Status)
}

// IsServerRejection reports whether err is a StatusError — i.e. the server
// answered with a non-2xx status (as opposed to a network/transport failure).
// Callers use it to decide whether re-entering a credential could help.
func IsServerRejection(err error) bool {
	var se *StatusError
	return errors.As(err, &se)
}

// Conn is a gated connection to one remote MCP server. Its transport is the
// ONE path any byte takes to the server; the ceiling fixed at construction
// bounds every call to the server's own host.
type Conn struct {
	server  Server
	guard   *gateway.Guard
	creds   *secret.Injector // host-owned credential jar; nil = no injection
	scanner *secret.Scanner  // bidirectional leak scanner; nil = no scanning
	http    *http.Client

	host    string
	ceiling capability.Ceiling
	client  *mcp.Client
}

// New builds a gated connection to srv. It parses the server host, fixes the
// connection's ceiling to exactly that host (http.read + http.write — the
// connection can never reach anywhere else, regardless of policy), and — if
// the server declares a credential (OAuth, or a static token) — binds its
// host-owned Bearer under owner "mcp:<name>". The binding only names the
// secret; the value is resolved at injection time (a refreshing OAuth source
// set by the caller, or the static token the caller stored in the vault).
// No I/O happens here; Connect performs the handshake.
func New(srv Server, guard *gateway.Guard, creds *secret.Injector, scanner *secret.Scanner, httpClient *http.Client) (*Conn, error) {
	u, err := url.Parse(srv.URL)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("mcpcap: server %q: bad url %q", srv.Name, srv.URL)
	}
	c := &Conn{
		server: srv, guard: guard, creds: creds, scanner: scanner, http: httpClient,
		host: u.Hostname(),
		ceiling: capability.NewCeiling(
			capability.Pair{Capability: "http.read", TargetGlob: u.Hostname()},
			capability.Pair{Capability: "http.write", TargetGlob: u.Hostname()},
		),
	}
	c.client = mcp.New(c.transport)
	if (srv.OAuth != nil || srv.Auth == "token") && creds != nil {
		creds.AddBinding(Owner(srv.Name), secret.Binding{
			Secret: SecretName(srv.Name, c.host), Capability: "http.write", Host: c.host,
			Header: "Authorization", Prefix: "Bearer ",
		})
	}
	return c, nil
}

// Host returns the server's hostname — the broker target every call of this
// connection is gated on.
func (c *Conn) Host() string { return c.host }

// Name returns the configured server name.
func (c *Conn) Name() string { return c.server.Name }

// transport is the gated HTTP leg under the protocol client, and the SOLE
// place the connection's ceiling and credential owner enter the request
// context — every protocol message (initialize, tools/list, tools/call)
// crosses the gateway here as an http.write to the server's host:
// ceiling-bounded, broker-gated (Ask → out-of-band HITL), leak-scanned on
// egress, credential-injected host-side, and redacted on ingress. Everything
// past the gate runs only if Do authorizes — a denied call never leaves the
// process.
func (c *Conn) transport(ctx context.Context, body []byte, header http.Header) (*mcp.Response, error) {
	ctx = capability.WithCeiling(ctx, c.ceiling)
	ctx = secret.WithOwner(ctx, Owner(c.server.Name))
	call := capability.Call{Capability: "http.write", Target: c.host}
	intent := "MCP " + c.server.Name + ": POST " + c.server.URL // overridden by the semantic WithIntent upstream
	return gateway.Do(ctx, c.guard, call, intent, func() (*mcp.Response, error) {
		// Egress leak scan on the model-reachable surfaces (URL, protocol headers,
		// and the JSON-RPC body — tool arguments ride in the body) BEFORE the
		// legitimate credential is stamped in, so the host's own bearer is never
		// flagged.
		parts := []string{c.server.URL, string(body)}
		for _, vs := range header {
			parts = append(parts, vs...)
		}
		if err := c.scanner.ScanEgress(parts...); err != nil {
			return nil, err
		}

		// Host-owned, capability-, host-, and owner-scoped credential injection:
		// only THIS connection's bearer (or an unowned app default) rides along.
		req := secret.Request{Method: http.MethodPost, URL: c.server.URL, Body: body, Headers: map[string]string{}}
		for k := range header {
			req.Headers[k] = header.Get(k)
		}
		if _, err := c.creds.InjectMatching(ctx, &req, call.Capability, c.host); err != nil {
			return nil, err
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.URL, bytes.NewReader(req.Body))
		if err != nil {
			return nil, err
		}
		for k, v := range req.Headers {
			httpReq.Header.Set(k, v)
		}

		client := c.http
		if client == nil {
			client = http.DefaultClient
		}
		resp, err := client.Do(httpReq)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return nil, &StatusError{Server: c.server.Name, Status: resp.StatusCode}
		}
		// Ingress redaction at the boundary, BEFORE protocol parsing — a stored
		// secret echoed back never reaches the model, whichever shape the server
		// chose. Both shapes are line-oriented (one JSON object, or SSE data:
		// frames each on one line), so per-line redaction is loss-free and keeps
		// the stream streaming.
		return &mcp.Response{
			ContentType: resp.Header.Get("Content-Type"),
			Header:      resp.Header,
			Body:        newRedactingReader(io.LimitReader(resp.Body, maxResponseBytes), resp.Body, c.scanner),
		}, nil
	})
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
