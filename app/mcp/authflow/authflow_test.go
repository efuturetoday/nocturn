package authflow_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/efuturetoday/nocturn/app/mcp/authflow"
)

func TestParseWWWAuthenticate(t *testing.T) {
	cases := map[string]struct {
		header string
		want   string
		ok     bool
	}{
		"standard":     {`Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource"`, "https://mcp.example.com/.well-known/oauth-protected-resource", true},
		"with realm":   {`Bearer realm="mcp", resource_metadata="https://mcp.example.com/prm"`, "https://mcp.example.com/prm", true},
		"no metadata":  {`Bearer realm="mcp", error="invalid_token"`, "", false},
		"not a bearer": {`Basic realm="x"`, "", false},
		"empty":        {``, "", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := authflow.ParseWWWAuthenticate(c.header)
			if ok != c.ok || got != c.want {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, ok, c.want, c.ok)
			}
		})
	}
}

// The full discovery chain over an httptest server acting as both the resource server
// (PRM) and the authorization server (ASM + registration).
func TestDiscoveryChain(t *testing.T) {
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, authflow.ProtectedResource{
			Resource:             srv.URL,
			AuthorizationServers: []string{srv.URL},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, authflow.AuthorizationServer{
			Issuer:                srv.URL,
			AuthorizationEndpoint: srv.URL + "/authorize",
			TokenEndpoint:         srv.URL + "/token",
			RegistrationEndpoint:  srv.URL + "/register",
			ScopesSupported:       []string{"repo", "read:user"},
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var req authflow.RegistrationRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.RedirectURIs) == 0 {
			t.Error("registration must send redirect_uris")
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, authflow.RegistrationResponse{ClientID: "dyn-client-123"})
	})
	srv = httptest.NewTLSServer(mux)
	defer srv.Close()

	c := authflow.New(srv.Client()) // srv.Client trusts the test TLS cert
	ctx := context.Background()

	// Step 1: PRM from the well-known (metadataURL "" → derived from the server URL).
	pr, err := c.ProtectedResourceMetadata(ctx, "", srv.URL)
	if err != nil {
		t.Fatalf("PRM: %v", err)
	}
	if len(pr.AuthorizationServers) != 1 || pr.AuthorizationServers[0] != srv.URL {
		t.Fatalf("PRM authorization_servers = %v", pr.AuthorizationServers)
	}

	// Step 2: ASM for the discovered authorization server.
	as, err := c.AuthorizationServerMetadata(ctx, pr.AuthorizationServers[0])
	if err != nil {
		t.Fatalf("ASM: %v", err)
	}
	if as.TokenEndpoint != srv.URL+"/token" || as.RegistrationEndpoint != srv.URL+"/register" {
		t.Fatalf("ASM = %+v", as)
	}

	// Step 3: Dynamic Client Registration.
	reg, err := c.Register(ctx, as.RegistrationEndpoint, authflow.RegistrationRequest{
		ClientName:              "nocturn",
		RedirectURIs:            []string{"http://127.0.0.1:54321/callback"},
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if reg.ClientID != "dyn-client-123" {
		t.Fatalf("client_id = %q", reg.ClientID)
	}
}

// A PRM document with no authorization_servers is rejected fail-closed.
func TestPRM_NoAuthServers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, authflow.ProtectedResource{Resource: "x"})
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	if _, err := authflow.New(srv.Client()).ProtectedResourceMetadata(context.Background(), "", srv.URL); err == nil {
		t.Fatal("PRM with no authorization_servers must error")
	}
}

// A non-https metadata URL is refused before any request.
func TestRefusesPlainHTTP(t *testing.T) {
	c := authflow.New(nil)
	if _, err := c.ProtectedResourceMetadata(context.Background(), "http://insecure/prm", ""); err == nil {
		t.Fatal("a non-https metadata URL must be refused")
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
