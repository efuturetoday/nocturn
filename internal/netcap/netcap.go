package netcap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// maxResponseBytes caps how much of a response Fetch reads into memory.
const maxResponseBytes = 10 << 20 // 10 MiB

// ErrManualCredential is returned when the guest-built request carries a
// credential itself — userinfo in the URL or a sensitive header. Credentials
// must flow only through host-side injection (the Injector), never smuggled by
// the model, or it could bypass the host's domain-bound credential control.
var ErrManualCredential = errors.New("netcap: request carries a manually-supplied credential")

// sensitiveHeaders are request headers the guest may never set itself; the host
// owns the credential channel.
var sensitiveHeaders = map[string]bool{
	"authorization":       true,
	"cookie":              true,
	"proxy-authorization": true,
	"x-api-key":           true,
}

// strippedResponseHeaders are response headers the guest never receives (matched
// case-insensitively): cookie-setting headers (the guest has no cookie jar and
// must not hoard credential material) and auth-challenge/credential headers (the
// host owns the credential channel). Every OTHER response header value is still
// leak-scanned before it reaches the model — this list is the small set we drop
// outright because they carry credential material by nature, not because of what
// they happen to contain.
var strippedResponseHeaders = map[string]bool{
	"set-cookie":         true,
	"set-cookie2":        true,
	"authorization":      true,
	"www-authenticate":   true,
	"proxy-authenticate": true,
}

// Net groups the networking capabilities. It holds a shared *gateway.Guard plus
// its own dependencies: the host-owned credential Injector (the "cookie jar") and
// an HTTP client. Further networking capabilities (dns, ping) are added as sibling
// methods here — the struct stays small and the Guard stays shared. Net is an
// interface-adapter: it maps a tool invocation to a capability + host and runs it
// through the guard, then performs the real I/O.
type Net struct {
	Guard       *gateway.Guard
	Credentials *secret.Injector // host-owned, domain-bound credential jar; nil = no injection
	Scanner     *secret.Scanner  // bidirectional secret leak scanner; nil = no scanning
	HTTP        *http.Client
	Resolver    *net.Resolver
}

// Response is the result of a gated outbound request, after ingress redaction.
// It carries the status and (redacted) response headers as well as the body, so
// a caller — the http tools, and through them fetch() in the guest — can be
// honest about the outcome instead of mistaking a 404 body for success.
type Response struct {
	Status     int               // HTTP status code
	StatusText string            // http.StatusText(Status)
	Headers    map[string]string // canonical key -> comma-joined values, redacted (credential/cookie headers dropped)
	Body       []byte            // <= maxResponseBytes, redacted
}

// Fetch performs an outbound HTTP request on the caller's behalf. The caller
// builds req WITHOUT credentials; the gateway injects any credential bound to
// the destination host at the boundary (the guest never sees the value, and
// never chooses the credential — the destination does). The request is gated on
// the destination host, so an unknown host escalates to human approval and a
// denied host never leaves the process.
func (n *Net) Fetch(ctx context.Context, req secret.Request) (*Response, error) {
	host, err := hostOf(req.URL)
	if err != nil {
		return nil, err
	}

	// The guest must not carry its own credential — that would bypass the
	// host-owned, domain-bound injection below.
	if err := rejectManualCredentials(req); err != nil {
		return nil, err
	}

	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	method = strings.ToUpper(method)

	// The mutation axis — read for safe methods, write for mutating ones — is what
	// the policy gates on; the family + host is the reach the cage and credential
	// bindings key on. The raw HTTP method never reaches the security layer.
	write := writeForMethod(method)
	call := capability.Call{Family: "http", Write: write, Target: host}
	intent := method + " " + req.URL

	// Everything past the gate runs only if Do authorizes AND the egress scan is
	// clean: the credential injection and the request itself are unreachable on a
	// denied or leaking call. The scan is declared via ScanEgress over the guest-
	// built request (URL + headers + body) and runs BEFORE the effect — hence before
	// the credential is injected below, so the host's own bearer is never flagged.
	return gateway.Do(ctx, n.Guard, call, intent,
		gateway.ScanEgress(n.Scanner, func() []string { return egressParts(req) }),
		func() (*Response, error) {
			// Host-owned, family- and host-scoped credential injection: only a
			// credential whose binding matches the "http" family AND destination host
			// rides along (a bearer is action-agnostic — same for read and write; nil
			// Injector = none).
			if _, err := n.Credentials.InjectMatching(ctx, &req, "http", host); err != nil {
				return nil, err
			}

			var body io.Reader
			if len(req.Body) > 0 {
				body = bytes.NewReader(req.Body)
			}
			httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, body)
			if err != nil {
				return nil, err
			}
			for k, v := range req.Headers {
				httpReq.Header.Set(k, v)
			}

			client := n.HTTP
			if client == nil {
				client = http.DefaultClient
			}
			resp, err := client.Do(httpReq)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
			if err != nil {
				return nil, err
			}
			// Ingress redaction: strip any secret echoed back before it reaches the
			// model — from the body AND from the response header values (a secret can
			// come back in a header too). Set-Cookie is dropped outright: the guest has
			// no cookie jar and raw cookies are a needless leak surface.
			hdr := make(map[string]string, len(resp.Header))
			for k, vs := range resp.Header {
				if strippedResponseHeaders[strings.ToLower(k)] {
					continue
				}
				hdr[k] = string(n.Scanner.RedactIngress([]byte(strings.Join(vs, ", "))))
			}
			return &Response{
				Status:     resp.StatusCode,
				StatusText: http.StatusText(resp.StatusCode),
				Headers:    hdr,
				Body:       n.Scanner.RedactIngress(respBody),
			}, nil
		})
}

// egressParts collects the guest-built request's leak-scannable surfaces.
func egressParts(req secret.Request) []string {
	parts := make([]string, 0, len(req.Headers)+2)
	parts = append(parts, req.URL)
	for _, v := range req.Headers {
		parts = append(parts, v)
	}
	if len(req.Body) > 0 {
		parts = append(parts, string(req.Body))
	}
	return parts
}

// writeForMethod maps an HTTP method to the mutation axis the broker gates on:
// safe methods are reads (false), mutating methods are writes (true). The mutation
// flag — not the raw verb — is what the policy keys on, keeping read/write a
// first-class authority and the HTTP method out of the security layer.
func writeForMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

// rejectManualCredentials refuses a guest-built request that carries a
// credential itself: userinfo in the URL (user:pass@host) or a sensitive header.
func rejectManualCredentials(req secret.Request) error {
	if u, err := url.Parse(req.URL); err == nil && u.User != nil {
		return fmt.Errorf("%w: userinfo in URL", ErrManualCredential)
	}
	for name := range req.Headers {
		if sensitiveHeaders[strings.ToLower(name)] {
			return fmt.Errorf("%w: %s header", ErrManualCredential, name)
		}
	}
	return nil
}

func hostOf(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("netcap: bad url %q: %w", rawURL, err)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("netcap: url %q has no host", rawURL)
	}
	return u.Hostname(), nil
}
