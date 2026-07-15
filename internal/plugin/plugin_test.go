package plugin_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/netcap"
	"github.com/efuturetoday/nocturn/internal/plugin"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/tool"
)

func validManifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "ok", Version: "1",
		Tools:    []plugin.ToolDecl{{Name: "t", Parameters: []byte(`{"type":"object"}`)}},
		Requires: []plugin.Require{{Capability: "http.read", Host: "x.com"}},
	}
}

func TestManifest_Validate_FailClosed(t *testing.T) {
	if err := validManifest().Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	bad := map[string]func(*plugin.Manifest){
		"empty name":     func(m *plugin.Manifest) { m.Name = "" },
		"spaced name":    func(m *plugin.Manifest) { m.Name = "Bad Name" },
		"no version":     func(m *plugin.Manifest) { m.Version = "" },
		"no tools":       func(m *plugin.Manifest) { m.Tools = nil },
		"dup tool":       func(m *plugin.Manifest) { m.Tools = append(m.Tools, m.Tools[0]) },
		"non-obj params": func(m *plugin.Manifest) { m.Tools[0].Parameters = []byte(`"nope"`) },
		"empty req cap":  func(m *plugin.Manifest) { m.Requires[0].Capability = "" },
		"empty req host": func(m *plugin.Manifest) { m.Requires[0].Host = "" },
	}
	for name, mut := range bad {
		m := validManifest()
		mut(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

func TestLoad_JSPlugin(t *testing.T) {
	l, err := plugin.Load("testdata/example")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if l.Kind != plugin.KindJS || l.Manifest.Name != "example" || len(l.Manifest.Tools) != 3 {
		t.Fatalf("loaded = %+v", l.Manifest)
	}
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type countNotifier struct {
	want    hitl.Outcome
	resolve func(token string) error
	calls   int
}

func (n *countNotifier) Notify(_ string, options []hitl.Option) error {
	n.calls++
	for _, o := range options {
		if o.Outcome == n.want {
			return n.resolve(o.Token)
		}
	}
	return errors.New("countNotifier: no matching option")
}

// End to end through the real QuickJS interpreter: a plugin's effect inside its
// ceiling asks once then goes silent after a session grant; an effect OUTSIDE the
// ceiling is hard-denied without ever asking; uninstall removes the tools.
func TestPlugin_CeilingBoundsEffects_E2E(t *testing.T) {
	stub := &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
	})}
	notifier := &countNotifier{want: hitl.ApprovedSession}
	engine := hitl.NewEngine([]byte("k"), notifier)
	notifier.resolve = engine.Resolve
	guard := &gateway.Guard{
		Policy: capability.Policy{Rules: []capability.Rule{
			{Capability: "http.read", HostGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
			{Capability: "http.write", HostGlob: capability.Wildcard, Effect: capability.Ask, Epoch: capability.Permanent},
		}},
		Approvals: engine,
		TTL:       time.Second,
	}
	netCap := &netcap.Net{Guard: guard, HTTP: stub, Scanner: secret.NewScanner(secret.NewStore())}
	reg := tool.NewRegistry(netCap.Tools())

	host := plugin.NewHost(reg, nil)
	l, err := plugin.Load("testdata/example")
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Install(l, func(plugin.Manifest) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !reg.Has("example.fetch") {
		t.Fatal("plugin tool example.fetch not registered")
	}

	// Session context owns the standing grants.
	ctx := capability.WithGrants(context.Background(), capability.NewGrants("test", capability.Permanent, nil))

	// 1. in-ceiling fetch → asks once → session grant → 2nd is silent.
	if out, err := reg.Invoke(ctx, "example.fetch", "{}"); err != nil || strings.TrimSpace(out) != "ok" {
		t.Fatalf("fetch 1: out=%q err=%v", out, err)
	}
	if notifier.calls != 1 {
		t.Fatalf("fetch 1 asked %d times, want 1", notifier.calls)
	}
	if _, err := reg.Invoke(ctx, "example.fetch", "{}"); err != nil {
		t.Fatalf("fetch 2: %v", err)
	}
	if notifier.calls != 1 {
		t.Fatalf("fetch 2 asked again (calls=%d) — the session grant should silence it", notifier.calls)
	}

	// 2. out-of-ceiling evil → hard denied, human NEVER asked; tool surfaces the error.
	before := notifier.calls
	if _, err := reg.Invoke(ctx, "example.evil", "{}"); err == nil {
		t.Fatal("evil (host outside the ceiling) must fail")
	}
	if notifier.calls != before {
		t.Fatalf("out-of-ceiling call asked the human (%d→%d), want 0 extra asks", before, notifier.calls)
	}

	// 3. uninstall removes the tools.
	if err := host.Uninstall("example"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if reg.Has("example.fetch") {
		t.Fatal("uninstall did not remove example.fetch")
	}
}
