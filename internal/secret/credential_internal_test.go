package secret

import (
	"context"
	"errors"
	"testing"
)

func TestHostMatches_EmptyOrStar_MatchesNothing(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		host    string
		want    bool
	}{
		{"empty pattern matches nothing", "", "api.example.com", false},
		{"bare star matches nothing", "*", "api.example.com", false},
		{"empty host never matches", "api.example.com", "", false},
		{"exact host matches", "api.example.com", "api.example.com", true},
		{"exact host mismatch", "api.example.com", "evil.example.com", false},
		{"wildcard suffix matches subdomain", "*.example.com", "api.example.com", true},
		{"wildcard suffix matches deep subdomain", "*.example.com", "a.b.example.com", true},
		{"wildcard suffix rejects bare suffix", "*.example.com", "example.com", false},
		{"wildcard suffix rejects unrelated", "*.example.com", "example.org", false},
		{"wildcard suffix empty host", "*.example.com", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hostMatches(tc.pattern, tc.host); got != tc.want {
				t.Fatalf("hostMatches(%q, %q) = %v, want %v", tc.pattern, tc.host, got, tc.want)
			}
		})
	}
}

// TestInjectMatching_MissingSource_FailClosed exercises the defensive branch
// where a binding has no registered resolver at all. The public API always seeds
// one, so we construct the injector directly to reach it — a binding without a
// source must fail closed with ErrNotFound, before any I/O.
func TestInjectMatching_MissingSource_FailClosed(t *testing.T) {
	in := &Injector{
		store:     NewStore(),
		resolvers: map[string]Resolver{}, // deliberately empty
		bindings: []ownedBinding{
			{owner: "", Binding: Binding{Secret: "api", Host: "api.example.com", Header: "Authorization"}},
		},
	}
	req := &Request{Method: "GET"}
	_, err := in.InjectMatching(context.Background(), req, "api.example.com")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing source: got %v, want ErrNotFound", err)
	}
	if _, ok := req.Headers["Authorization"]; ok {
		t.Fatal("a header was stamped despite a missing source")
	}
}

func TestSetOwned_DropsBindingsAndPrivateResolver(t *testing.T) {
	t.Run("sole owner removed drops binding and resolver", func(t *testing.T) {
		in := NewInjector(NewStore())
		in.SetOwned(map[string][]Binding{"plugin:a": {{Secret: "a-tok", Host: "api.com", Header: "H"}}})
		in.SetResolver("a-tok", staticResolverInternal{val: []byte("v")})

		in.SetOwned(nil)

		if len(in.bindings) != 0 {
			t.Fatalf("bindings remained after removal: %v", in.bindings)
		}
		if _, ok := in.resolvers["a-tok"]; ok {
			t.Fatal("owner-private resolver not dropped on uninstall")
		}
	})

	t.Run("resolver kept while another binding still uses the secret", func(t *testing.T) {
		in := NewInjector(NewStore(), Binding{Secret: "a-tok", Host: "api.com", Header: "H"}) // app default
		in.SetOwned(map[string][]Binding{"plugin:a": {{Secret: "a-tok", Host: "api.com", Header: "H2"}}})
		in.SetResolver("a-tok", staticResolverInternal{val: []byte("v")})

		in.SetOwned(nil)

		if len(in.bindings) != 1 {
			t.Fatalf("wrong binding count after removal: %d", len(in.bindings))
		}
		if _, ok := in.resolvers["a-tok"]; !ok {
			t.Fatal("resolver dropped while a remaining binding still uses the secret")
		}
	})

	// The reason SetOwned keeps resolvers at all: an OAuth credential's resolver REFRESHES the token
	// and is registered elsewhere in the discovery pass. Replacing it with a static store read on
	// every reload would stamp the stored token JSON — refresh token included — into a header.
	t.Run("a refreshing resolver survives a rebind of the same secret", func(t *testing.T) {
		in := NewInjector(NewStore())
		bindings := map[string][]Binding{"mcp:x": {{Secret: "mcp:x@h/oauth", Host: "h", Header: "Authorization"}}}
		in.SetOwned(bindings)
		in.SetResolver("mcp:x@h/oauth", staticResolverInternal{val: []byte("refreshed")})

		in.SetOwned(bindings) // the next discovery pass

		req := &Request{Method: "GET", Headers: map[string]string{}}
		if _, err := in.InjectMatching(WithOwner(context.Background(), "mcp:x"), req, "h"); err != nil {
			t.Fatalf("InjectMatching: %v", err)
		}
		if got := req.Headers["Authorization"]; got != "refreshed" {
			t.Fatalf("Authorization = %q; the refreshing resolver was replaced by a static store read", got)
		}
	})
}

// TestAmbientRidesOnlyTheUnownedCaller: ambient means the model's own tool call, not "everybody". A
// plugin guest carries its owner on the context, and must not pick up a skill's credential.
func TestAmbientRidesOnlyTheUnownedCaller(t *testing.T) {
	store := NewStore()
	store.Set("skill:house@h/token", []byte("s3cr3t"))
	in := NewInjector(store)
	in.SetOwned(map[string][]Binding{
		"skill:house": {{Secret: "skill:house@h/token", Host: "h", Header: "Authorization", Ambient: true}},
	})

	model := &Request{Method: "GET", Headers: map[string]string{}}
	if _, err := in.InjectMatching(context.Background(), model, "h"); err != nil {
		t.Fatalf("InjectMatching (model): %v", err)
	}
	if model.Headers["Authorization"] != "s3cr3t" {
		t.Fatal("the model's own call did not carry the skill's credential")
	}

	guest := &Request{Method: "GET", Headers: map[string]string{}}
	if _, err := in.InjectMatching(WithOwner(context.Background(), "plugin:other"), guest, "h"); err != nil {
		t.Fatalf("InjectMatching (plugin): %v", err)
	}
	if _, ok := guest.Headers["Authorization"]; ok {
		t.Fatal("a plugin guest picked up a skill's ambient credential")
	}
}

// TestHostMatchIsCaseInsensitive: the two sides come from different worlds — a declaration somebody
// wrote and whatever URL the caller built. A mismatch here is silent: the request simply leaves
// unauthenticated.
func TestHostMatchIsCaseInsensitive(t *testing.T) {
	store := NewStore()
	store.Set("tok", []byte("v"))
	in := NewInjector(store, Binding{Secret: "tok", Host: "api.example.com", Header: "Authorization"})

	req := &Request{Method: "GET", Headers: map[string]string{}}
	if _, err := in.InjectMatching(context.Background(), req, "API.Example.com"); err != nil {
		t.Fatalf("InjectMatching: %v", err)
	}
	if req.Headers["Authorization"] != "v" {
		t.Fatal("a mixed-case host did not match its binding")
	}
}

type staticResolverInternal struct{ val []byte }

func (s staticResolverInternal) Value(context.Context) ([]byte, error) { return s.val, nil }
