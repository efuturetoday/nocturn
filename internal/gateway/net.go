package gateway

import (
	"context"
	"io"
	"net"
	"net/http"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// maxResponseBytes caps how much of a response Fetch reads into memory.
const maxResponseBytes = 10 << 20 // 10 MiB

// Net groups the networking capabilities. It holds a shared *Guard plus its own
// dependencies (a secret store, an HTTP client). Further networking capabilities
// (dns, ping) are added as sibling methods here — the struct stays small and the
// Guard stays shared.
type Net struct {
	Guard    *Guard
	Secrets  *secret.Store
	HTTP     *http.Client
	Resolver *net.Resolver
}

// Fetch performs an outbound HTTP request on the caller's behalf. The caller
// builds req WITHOUT credentials; if binding is non-nil the gateway injects the
// bound secret at the boundary. The request is gated on the destination host,
// so an unknown host escalates to human approval and a denied host never leaves
// the process.
func (n *Net) Fetch(ctx context.Context, req secret.Request, binding *secret.Binding) ([]byte, error) {
	host, err := hostOf(req.URL)
	if err != nil {
		return nil, err
	}

	call := capability.Call{Capability: "net.fetch", Attrs: map[string]string{"host": host}}
	if err := n.Guard.Authorize(ctx, call, "fetch "+req.URL); err != nil {
		return nil, err
	}

	if binding != nil {
		if err := secret.Inject(n.Secrets, &req, *binding); err != nil {
			return nil, err
		}
	}

	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, nil)
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
	return io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
}

// Resolve looks up the addresses for a hostname. Like Fetch it is gated on the
// host — DNS is an exfiltration channel (a lookup to an attacker's nameserver
// leaks whatever is encoded in the query), so an unknown host escalates to
// approval. This is a sibling capability sharing the same Guard: adding it grew
// no god-object, just a small method with its own dependency (a resolver).
func (n *Net) Resolve(ctx context.Context, host string) ([]string, error) {
	call := capability.Call{Capability: "dns.resolve", Attrs: map[string]string{"host": host}}
	if err := n.Guard.Authorize(ctx, call, "resolve "+host); err != nil {
		return nil, err
	}

	resolver := n.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return resolver.LookupHost(ctx, host)
}
