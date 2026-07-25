package secret_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/secret"
)

// staticResolver is a fake Resolver yielding a fixed value.
type staticResolver struct{ val []byte }

func (s staticResolver) Value(context.Context) ([]byte, error) { return s.val, nil }

// errResolver is a fake Resolver that always fails.
type errResolver struct{ err error }

func (e errResolver) Value(context.Context) ([]byte, error) { return nil, e.err }

func TestInjectMatching_StampsBearerHostSide(t *testing.T) {
	store := secret.NewStore()
	store.Set("api", []byte("s3cr3t-token"))
	in := secret.NewInjector(store, secret.Binding{
		Secret: "api", Host: "api.example.com", Header: "Authorization", Prefix: "Bearer ",
	})

	req := &secret.Request{Method: "GET", URL: "https://api.example.com/x"}
	injected, err := in.InjectMatching(context.Background(), req, "api.example.com")
	if err != nil {
		t.Fatalf("InjectMatching: %v", err)
	}
	if got := req.Headers["Authorization"]; got != "Bearer s3cr3t-token" {
		t.Fatalf("header = %q, want %q", got, "Bearer s3cr3t-token")
	}
	if len(injected) != 1 || injected[0] != "api" {
		t.Fatalf("injected = %v, want [api]", injected)
	}
}

func TestInjectMatching_HostMismatch_NoInjection(t *testing.T) {
	store := secret.NewStore()
	store.Set("api", []byte("token"))
	in := secret.NewInjector(store, secret.Binding{
		Secret: "api", Host: "api.example.com", Header: "Authorization",
	})

	req := &secret.Request{Method: "GET", URL: "https://evil.example.org/x"}
	injected, err := in.InjectMatching(context.Background(), req, "evil.example.org")
	if err != nil {
		t.Fatalf("InjectMatching: %v", err)
	}
	if len(injected) != 0 {
		t.Fatalf("injected on a mismatched host: %v", injected)
	}
	if _, ok := req.Headers["Authorization"]; ok {
		t.Fatal("credential stamped for the wrong destination")
	}
}

func TestInjectMatching_ResolverError_FailClosed(t *testing.T) {
	store := secret.NewStore()
	in := secret.NewInjector(store, secret.Binding{
		Secret: "api", Host: "api.example.com", Header: "Authorization",
	})
	in.SetResolver("api", errResolver{err: errors.New("refresh failed")})

	req := &secret.Request{Method: "GET"}
	injected, err := in.InjectMatching(context.Background(), req, "api.example.com")
	if err == nil {
		t.Fatal("resolver error did not fail the injection")
	}
	if len(injected) != 0 {
		t.Fatalf("injected despite resolver error: %v", injected)
	}
	if _, ok := req.Headers["Authorization"]; ok {
		t.Fatal("a half-authenticated header was left behind")
	}
}

func TestInjectMatching_StoreResolver_ManualCredMissing_ErrNotFound(t *testing.T) {
	store := secret.NewStore() // secret never stored
	in := secret.NewInjector(store, secret.Binding{
		Secret: "api", Host: "api.example.com", Header: "Authorization",
	})
	req := &secret.Request{Method: "GET"}
	_, err := in.InjectMatching(context.Background(), req, "api.example.com")
	if !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("missing store credential: got %v, want ErrNotFound", err)
	}
}

func TestInjectMatching_OwnerScoping_NoCrossPluginBleed(t *testing.T) {
	store := secret.NewStore()
	store.Set("a-tok", []byte("plugin-a-token"))
	in := secret.NewInjector(store)
	in.AddBinding("pluginA", secret.Binding{
		Secret: "a-tok", Host: "shared.example.com", Header: "Authorization", Prefix: "Bearer ",
	})

	cases := []struct {
		name    string
		owner   string
		wantHit bool
	}{
		{"owner A rides its own binding", "pluginA", true},
		{"owner B cannot pick up A's token", "pluginB", false},
		{"unowned caller sees no owned binding", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.owner != "" {
				ctx = secret.WithOwner(ctx, tc.owner)
			}
			req := &secret.Request{Method: "GET"}
			injected, err := in.InjectMatching(ctx, req, "shared.example.com")
			if err != nil {
				t.Fatalf("InjectMatching: %v", err)
			}
			_, stamped := req.Headers["Authorization"]
			if stamped != tc.wantHit || (len(injected) > 0) != tc.wantHit {
				t.Fatalf("owner %q: stamped=%v injected=%v, want hit=%v", tc.owner, stamped, injected, tc.wantHit)
			}
		})
	}
}

func TestInjectMatching_NilInjector_NoOp(t *testing.T) {
	var in *secret.Injector
	req := &secret.Request{Method: "GET"}
	injected, err := in.InjectMatching(context.Background(), req, "api.example.com")
	if err != nil || injected != nil {
		t.Fatalf("nil injector: got (%v, %v), want (nil, nil)", injected, err)
	}
}

func TestInjectMatching_SetResolver_OverridesWithDynamicSource(t *testing.T) {
	store := secret.NewStore()
	store.Set("api", []byte("static-from-store"))
	in := secret.NewInjector(store, secret.Binding{
		Secret: "api", Host: "api.example.com", Header: "Authorization",
	})
	in.SetResolver("api", staticResolver{val: []byte("dynamic-refreshed")})

	req := &secret.Request{Method: "GET"}
	if _, err := in.InjectMatching(context.Background(), req, "api.example.com"); err != nil {
		t.Fatalf("InjectMatching: %v", err)
	}
	if got := req.Headers["Authorization"]; got != "dynamic-refreshed" {
		t.Fatalf("header = %q, want the dynamic source value", got)
	}
}

// reentrantResolver calls back into the injector during Value. If InjectMatching
// resolved WHILE holding its mutex, this would deadlock (sync.Mutex is not
// reentrant) — so completing proves resolver I/O runs outside the lock.
type reentrantResolver struct {
	in  *secret.Injector
	val []byte
}

func (r reentrantResolver) Value(context.Context) ([]byte, error) {
	r.in.SetResolver("other", staticResolver{val: []byte("x")}) // needs the injector lock
	return r.val, nil
}

func TestInjectMatching_ResolverIOOutsideLock(t *testing.T) {
	store := secret.NewStore()
	in := secret.NewInjector(store, secret.Binding{
		Secret: "api", Host: "api.example.com", Header: "Authorization",
	})
	in.SetResolver("api", reentrantResolver{in: in, val: []byte("tok")})

	done := make(chan error, 1)
	go func() {
		req := &secret.Request{Method: "GET"}
		_, err := in.InjectMatching(context.Background(), req, "api.example.com")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("InjectMatching: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("InjectMatching deadlocked — resolver was called under the lock")
	}
}

func TestInjectMatching_ApplyTo_InitializesHeaderMap(t *testing.T) {
	store := secret.NewStore()
	store.Set("api", []byte("token"))
	in := secret.NewInjector(store, secret.Binding{
		Secret: "api", Host: "api.example.com", Header: "Authorization", Prefix: "Bearer ",
	})
	req := &secret.Request{Method: "GET"} // Headers is nil
	if _, err := in.InjectMatching(context.Background(), req, "api.example.com"); err != nil {
		t.Fatalf("InjectMatching: %v", err)
	}
	if req.Headers == nil || req.Headers["Authorization"] != "Bearer token" {
		t.Fatalf("nil header map not initialized: %v", req.Headers)
	}
}

func TestInjectMatching_MultipleMatches_AllStamped(t *testing.T) {
	store := secret.NewStore()
	store.Set("a", []byte("aaa"))
	store.Set("b", []byte("bbb"))
	in := secret.NewInjector(store,
		secret.Binding{Secret: "a", Host: "api.example.com", Header: "X-A"},
		secret.Binding{Secret: "b", Host: "api.example.com", Header: "X-B"},
	)
	req := &secret.Request{Method: "GET"}
	injected, err := in.InjectMatching(context.Background(), req, "api.example.com")
	if err != nil {
		t.Fatalf("InjectMatching: %v", err)
	}
	if req.Headers["X-A"] != "aaa" || req.Headers["X-B"] != "bbb" {
		t.Fatalf("not all matches stamped: %v", req.Headers)
	}
	if len(injected) != 2 {
		t.Fatalf("injected = %v, want 2 names", injected)
	}
}
