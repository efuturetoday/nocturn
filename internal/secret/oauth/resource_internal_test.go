package oauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The RFC 8707 resource indicator MUST appear in the authorization request when set,
// and MUST be omitted when empty (a non-MCP provider that does not use it). AuthCodeURL
// also pins the redirect_uri it was handed, so a later Exchange matches.
func TestAuthCodeURL_ResourceParam(t *testing.T) {
	cfg := Provider("https://as.example/authorize", "https://as.example/token", "cid", "")

	got := AuthCodeURL(cfg, "state123", "verifier", "https://mcp.example.com/mcp", "http://127.0.0.1:9/callback")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	q := u.Query()
	if q.Get("resource") != "https://mcp.example.com/mcp" {
		t.Errorf("resource = %q, want the MCP server URI", q.Get("resource"))
	}
	if q.Get("redirect_uri") != "http://127.0.0.1:9/callback" {
		t.Errorf("redirect_uri = %q, want the passed loopback", q.Get("redirect_uri"))
	}
	// Sanity: PKCE challenge + method are present (S256), and the state rides along.
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		t.Errorf("PKCE S256 challenge missing: %v", q)
	}
	if q.Get("state") != "state123" {
		t.Errorf("state = %q", q.Get("state"))
	}

	// Empty resource → the parameter is absent entirely.
	bare := AuthCodeURL(cfg, "s", "v", "", "http://127.0.0.1:9/callback")
	if strings.Contains(bare, "resource=") {
		t.Errorf("empty resource must omit the parameter: %s", bare)
	}
}

// Exchange carries the resource indicator and the PKCE verifier into the token request
// (the MCP spec requires resource on BOTH the authorization and token requests), and
// omits resource when empty.
func TestExchange_ResourceParam(t *testing.T) {
	for _, tc := range []struct {
		name, resource, wantResource string
	}{
		{"with resource", "https://mcp.example.com/mcp", "https://mcp.example.com/mcp"},
		{"empty resource", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen url.Values
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				seen, _ = url.ParseQuery(string(b))
				w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
				_, _ = w.Write([]byte("access_token=at&token_type=bearer"))
			}))
			defer srv.Close()

			cfg := Provider("https://as.example/authorize", srv.URL, "cid", "")
			if _, err := Exchange(context.Background(), cfg, "the-code", "verifier", tc.resource); err != nil {
				t.Fatalf("Exchange: %v", err)
			}
			if seen.Get("resource") != tc.wantResource {
				t.Errorf("token resource = %q, want %q", seen.Get("resource"), tc.wantResource)
			}
			if seen.Get("code_verifier") != "verifier" {
				t.Errorf("PKCE verifier missing from token request: %v", seen)
			}
			if seen.Get("code") != "the-code" {
				t.Errorf("code = %q, want the-code", seen.Get("code"))
			}
		})
	}
}

// resourceInjector adds the RFC 8707 resource indicator to a form-encoded token POST
// (a refresh request), and leaves non-token requests untouched.
func TestResourceInjector(t *testing.T) {
	const res = "https://mcp.example.com/mcp"
	var seen url.Values
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(req.Body)
		seen, _ = url.ParseQuery(string(b))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})
	inj := resourceInjector{resource: res, base: base}

	req, _ := http.NewRequest(http.MethodPost, "https://as.example/token", strings.NewReader("grant_type=refresh_token&refresh_token=r"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := inj.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if seen.Get("resource") != res {
		t.Errorf("refresh resource = %q, want %q", seen.Get("resource"), res)
	}
	if seen.Get("grant_type") != "refresh_token" {
		t.Errorf("original form fields lost: %v", seen)
	}

	seen = nil
	req2, _ := http.NewRequest(http.MethodPost, "https://as.example/token", strings.NewReader("grant_type=refresh_token"))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_, _ = resourceInjector{resource: "", base: base}.RoundTrip(req2)
	if seen.Get("resource") != "" {
		t.Errorf("empty resource must inject nothing, got %q", seen.Get("resource"))
	}
}
