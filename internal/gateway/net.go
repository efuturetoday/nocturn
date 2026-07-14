package gateway

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
	"github.com/efuturetoday/nocturn/internal/secret"
)

// maxResponseBytes caps how much of a response Fetch reads into memory.
const maxResponseBytes = 10 << 20 // 10 MiB

// ErrManualCredential is returned when the guest-built request carries a
// credential itself — userinfo in the URL or a sensitive header. Credentials
// must flow only through host-side injection (the Injector), never smuggled by
// the model, or it could bypass the host's domain-bound credential control.
var ErrManualCredential = errors.New("gateway: request carries a manually-supplied credential")

// sensitiveHeaders are request headers the guest may never set itself; the host
// owns the credential channel.
var sensitiveHeaders = map[string]bool{
	"authorization":       true,
	"cookie":              true,
	"proxy-authorization": true,
	"x-api-key":           true,
}

// Net groups the networking capabilities. It holds a shared *Guard plus its own
// dependencies: the host-owned credential Injector (the "cookie jar") and an
// HTTP client. Further networking capabilities (dns, ping) are added as sibling
// methods here — the struct stays small and the Guard stays shared.
type Net struct {
	Guard       *Guard
	Credentials *secret.Injector // host-owned, domain-bound credential jar; nil = no injection
	Scanner     *secret.Scanner  // bidirectional secret leak scanner; nil = no scanning
	HTTP        *http.Client
	Resolver    *net.Resolver
}

// Fetch performs an outbound HTTP request on the caller's behalf. The caller
// builds req WITHOUT credentials; the gateway injects any credential bound to
// the destination host at the boundary (the guest never sees the value, and
// never chooses the credential — the destination does). The request is gated on
// the destination host, so an unknown host escalates to human approval and a
// denied host never leaves the process.
func (n *Net) Fetch(ctx context.Context, req secret.Request) ([]byte, error) {
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

	// The capability — http.read for safe methods, http.write for mutating ones —
	// is what the policy and credential bindings key on. The raw HTTP method
	// never reaches the security layer.
	capName := capabilityForMethod(method)
	call := capability.Call{Capability: capName, Attrs: map[string]string{"host": host}}

	// Everything past the gate runs only if Do authorizes: the leak-scan,
	// credential injection, and the request itself are unreachable on a denied
	// call. Keeping them inside the closure makes the guarded pipeline cohesive
	// and a bypass impossible by construction.
	return Do(n.Guard, ctx, call, method+" "+req.URL, func() ([]byte, error) {
		// Egress leak scan on the guest-built request (URL + headers + body), BEFORE
		// the legitimate credential is stamped in below — so the host's own injected
		// bearer is never flagged.
		if err := n.Scanner.ScanEgress(egressParts(req)...); err != nil {
			return nil, err
		}

		// Host-owned, capability- and host-scoped credential injection: only a
		// credential whose binding matches this capability AND destination host
		// rides along (nil Injector = none).
		if _, err := n.Credentials.InjectMatching(&req, capName, host); err != nil {
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
		// Ingress redaction: strip any secret echoed back before it reaches the model.
		return n.Scanner.RedactIngress(respBody), nil
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

// capabilityForMethod maps an HTTP method to the capability that gates it: safe
// (read) methods to http.read, mutating (write) methods to http.write. The
// capability — not the raw verb — is what the policy and credential bindings key
// on, keeping read/write a first-class authority and the HTTP method out of the
// security layer.
func capabilityForMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return "http.read"
	default:
		return "http.write"
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

// Resolve looks up the addresses for a hostname. Like Fetch it is gated on the
// host — DNS is an exfiltration channel (a lookup to an attacker's nameserver
// leaks whatever is encoded in the query), so an unknown host escalates to
// approval. This is a sibling capability sharing the same Guard: adding it grew
// no god-object, just a small method with its own dependency (a resolver).
func (n *Net) Resolve(ctx context.Context, host string) ([]string, error) {
	call := capability.Call{Capability: "dns.resolve", Attrs: map[string]string{"host": host}}
	return Do(n.Guard, ctx, call, "resolve "+host, func() ([]string, error) {
		resolver := n.Resolver
		if resolver == nil {
			resolver = net.DefaultResolver
		}
		return resolver.LookupHost(ctx, host)
	})
}
