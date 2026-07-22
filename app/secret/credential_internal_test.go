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

func TestRemoveBindingsFor_DropsBindingsAndPrivateResolver(t *testing.T) {
	t.Run("sole owner removed drops binding and resolver", func(t *testing.T) {
		in := NewInjector(NewStore())
		in.AddBinding("pluginA", Binding{Secret: "a-tok", Host: "api.com", Header: "H"})
		in.SetResolver("a-tok", staticResolverInternal{val: []byte("v")})

		in.RemoveBindingsFor("pluginA")

		if len(in.bindings) != 0 {
			t.Fatalf("bindings remained after removal: %v", in.bindings)
		}
		if _, ok := in.resolvers["a-tok"]; ok {
			t.Fatal("owner-private resolver not dropped on uninstall")
		}
	})

	t.Run("resolver kept while another binding still uses the secret", func(t *testing.T) {
		in := NewInjector(NewStore())
		in.AddBinding("", Binding{Secret: "a-tok", Host: "api.com", Header: "H"})        // app default
		in.AddBinding("pluginA", Binding{Secret: "a-tok", Host: "api.com", Header: "H2"}) // plugin
		in.SetResolver("a-tok", staticResolverInternal{val: []byte("v")})

		in.RemoveBindingsFor("pluginA")

		if len(in.bindings) != 1 {
			t.Fatalf("wrong binding count after removal: %d", len(in.bindings))
		}
		if _, ok := in.resolvers["a-tok"]; !ok {
			t.Fatal("resolver dropped while a remaining binding still uses the secret")
		}
	})
}

type staticResolverInternal struct{ val []byte }

func (s staticResolverInternal) Value(context.Context) ([]byte, error) { return s.val, nil }
