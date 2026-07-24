// Package authflow implements the discovery half of the MCP authorization spec
// (2025-06-18): the client-side steps that turn a bare MCP server URL into concrete
// OAuth endpoints and a client id, so nocturn can run the interactive flow without
// hand-configured endpoints.
//
// The chain, per the spec:
//  1. an unauthenticated request returns 401 with a WWW-Authenticate header naming
//     the Protected Resource Metadata URL — ParseWWWAuthenticate extracts it;
//  2. that metadata (RFC 9728) lists the authorization server(s) — ProtectedResource;
//  3. the authorization server's metadata (RFC 8414) gives the authorization, token,
//     and (optional) dynamic-registration endpoints — AuthorizationServer;
//  4. if the server supports it, Dynamic Client Registration (RFC 7591) mints a
//     client id without a human pre-registering one — Register.
//
// This package is pure HTTP+JSON over an injected client; it runs no browser flow and
// holds no secrets. All fetched URLs must be https (a token must never chase a
// cleartext endpoint), and every body is size-capped.
package authflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// maxMetaBytes caps a metadata/registration response — a bomb guard, far above any
// real document.
const maxMetaBytes = 1 << 20 // 1 MiB

// Client fetches OAuth discovery documents over an injected HTTP client.
type Client struct{ http *http.Client }

// New returns a Client over h (defaults to http.DefaultClient when nil).
func New(h *http.Client) *Client {
	if h == nil {
		h = http.DefaultClient
	}
	return &Client{http: h}
}

// ProtectedResource is the RFC 9728 Protected Resource Metadata a MCP server serves,
// naming the authorization server(s) that issue tokens for it.
type ProtectedResource struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
}

// AuthorizationServer is the RFC 8414 metadata: the endpoints a client drives.
type AuthorizationServer struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint,omitempty"`
	ScopesSupported       []string `json:"scopes_supported,omitempty"`
}

// ParseWWWAuthenticate extracts the resource_metadata URL from a Bearer challenge
// (RFC 9728 §5.1), e.g. `Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource"`.
// ok is false when the header carries no such parameter.
func ParseWWWAuthenticate(header string) (resourceMetadataURL string, ok bool) {
	// The header is a scheme followed by comma-separated key="value" params; find the
	// resource_metadata one without over-parsing the auth-param grammar.
	for part := range strings.SplitSeq(header, ",") {
		part = strings.TrimSpace(part)
		// Drop a leading scheme token ("Bearer ") if present on the first part.
		if i := strings.IndexByte(part, ' '); i >= 0 && !strings.Contains(part[:i], "=") {
			part = strings.TrimSpace(part[i+1:])
		}
		k, v, found := strings.Cut(part, "=")
		if !found || strings.TrimSpace(k) != "resource_metadata" {
			continue
		}
		u := strings.Trim(strings.TrimSpace(v), `"`)
		if u != "" {
			return u, true
		}
	}
	return "", false
}

// ProbeResourceMetadata makes an unauthenticated request to the MCP server and, on the
// 401 the spec requires, returns the resource_metadata URL from the WWW-Authenticate
// challenge (the canonical discovery trigger, RFC 9728 §5.1). ok is false when the
// server does not answer 401 with a resource_metadata pointer — the caller then falls
// back to the well-known location.
func (c *Client) ProbeResourceMetadata(ctx context.Context, serverURL string) (metadataURL string, ok bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL, nil)
	if err != nil {
		return "", false
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", false
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		return "", false
	}
	return ParseWWWAuthenticate(resp.Header.Get("WWW-Authenticate"))
}

// ProtectedResourceMetadata fetches RFC 9728 metadata. metadataURL is the URL from the
// WWW-Authenticate header; when "", it tries the well-known locations derived from
// serverURL — the path-aware form (RFC 9728 §3: the well-known segment is inserted
// between host and path) then the origin form.
func (c *Client) ProtectedResourceMetadata(ctx context.Context, metadataURL, serverURL string) (*ProtectedResource, error) {
	var candidates []string
	if metadataURL != "" {
		candidates = []string{metadataURL}
	} else {
		wk, err := prmWellKnownURLs(serverURL)
		if err != nil {
			return nil, err
		}
		candidates = wk
	}
	var lastErr error
	for _, u := range candidates {
		var pr ProtectedResource
		if err := c.getJSON(ctx, u, &pr); err != nil {
			lastErr = err
			continue
		}
		if len(pr.AuthorizationServers) == 0 {
			lastErr = fmt.Errorf("protected resource metadata at %s: no authorization_servers", u)
			continue
		}
		return &pr, nil
	}
	return nil, fmt.Errorf("protected resource metadata: %w", lastErr)
}

// prmWellKnownURLs returns the RFC 9728 well-known candidates for a resource URL, in
// preference order: the path-aware form, then the origin form.
func prmWellKnownURLs(serverURL string) ([]string, error) {
	u, err := url.Parse(serverURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("bad server URL %q", serverURL)
	}
	origin := u.Scheme + "://" + u.Host
	var out []string
	if path := strings.TrimSuffix(u.Path, "/"); path != "" {
		out = append(out, origin+"/.well-known/oauth-protected-resource"+path)
	}
	out = append(out, origin+"/.well-known/oauth-protected-resource")
	return out, nil
}

// AuthorizationServerMetadata fetches RFC 8414 metadata for an issuer, trying the
// oauth-authorization-server well-known (path-aware, then origin) and the OpenID
// Connect discovery document as a fallback. The first that yields a usable document
// (with authorization + token endpoints) wins.
func (c *Client) AuthorizationServerMetadata(ctx context.Context, issuer string) (*AuthorizationServer, error) {
	candidates, err := asMetadataURLs(issuer)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, u := range candidates {
		var as AuthorizationServer
		if err := c.getJSON(ctx, u, &as); err != nil {
			lastErr = err
			continue
		}
		if as.AuthorizationEndpoint == "" || as.TokenEndpoint == "" {
			lastErr = fmt.Errorf("authorization server metadata at %s missing endpoints", u)
			continue
		}
		if !isHTTPS(as.AuthorizationEndpoint) || !isHTTPS(as.TokenEndpoint) {
			return nil, fmt.Errorf("authorization server metadata: endpoints must be https")
		}
		return &as, nil
	}
	return nil, fmt.Errorf("authorization server metadata for %q: %w", issuer, lastErr)
}

// getJSON GETs an https URL and decodes a JSON body into v, size-capped.
func (c *Client) getJSON(ctx context.Context, rawURL string, v any) error {
	if !isHTTPS(rawURL) {
		return fmt.Errorf("refusing non-https metadata URL %q", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", rawURL, resp.StatusCode)
	}
	return decodeJSON(resp.Body, v)
}

func decodeJSON(r io.Reader, v any) error {
	data, err := io.ReadAll(io.LimitReader(r, maxMetaBytes))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// asMetadataURLs returns the RFC 8414 / OIDC well-known candidates for an issuer, in
// preference order. RFC 8414 inserts the well-known segment between host and path.
func asMetadataURLs(issuer string) ([]string, error) {
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("bad issuer %q (want an https URL)", issuer)
	}
	path := strings.TrimSuffix(u.Path, "/")
	origin := u.Scheme + "://" + u.Host
	var out []string
	if path != "" { // path-aware form: /.well-known/oauth-authorization-server<path>
		out = append(out,
			origin+"/.well-known/oauth-authorization-server"+path,
			origin+path+"/.well-known/openid-configuration",
		)
	}
	out = append(out,
		origin+"/.well-known/oauth-authorization-server",
		origin+"/.well-known/openid-configuration",
	)
	return out, nil
}

// CanonicalResource returns the RFC 8707 canonical resource identifier for an MCP
// server URL: lowercase scheme + host, no fragment or query, and no trailing slash
// (the form the MCP spec prefers). It is sent as the resource indicator so the issued
// token is bound to exactly this server — e.g. https://API.githubcopilot.com/mcp/
// becomes https://api.githubcopilot.com/mcp.
func CanonicalResource(serverURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("bad server URL %q", serverURL)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	u.RawQuery = ""
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u.String(), nil
}

func isHTTPS(rawURL string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

// --- Dynamic Client Registration (RFC 7591) ---

// RegistrationRequest is the RFC 7591 client metadata nocturn registers. The redirect
// URIs must be filled by the caller with the exact loopback URL the flow will use.
type RegistrationRequest struct {
	ClientName              string   `json:"client_name,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types,omitempty"`
	ResponseTypes           []string `json:"response_types,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
	Scope                   string   `json:"scope,omitempty"` // space-delimited
}

// RegistrationResponse is the RFC 7591 result. ClientSecret is empty for a public
// (PKCE) client.
type RegistrationResponse struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
}

// Register performs RFC 7591 Dynamic Client Registration at endpoint, returning the
// minted client id (and secret, if any). endpoint must be https.
func (c *Client) Register(ctx context.Context, endpoint string, req RegistrationRequest) (*RegistrationResponse, error) {
	if !isHTTPS(endpoint) {
		return nil, fmt.Errorf("refusing non-https registration endpoint %q", endpoint)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// RFC 7591: 201 Created on success; some servers return 200.
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dynamic client registration: HTTP %d", resp.StatusCode)
	}
	var out RegistrationResponse
	if err := decodeJSON(resp.Body, &out); err != nil {
		return nil, err
	}
	if out.ClientID == "" {
		return nil, fmt.Errorf("dynamic client registration: no client_id in response")
	}
	return &out, nil
}
