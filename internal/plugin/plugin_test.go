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
		Tools: []plugin.ToolDecl{{Name: "t", Parameters: []byte(`{"type":"object"}`)}},
		Cage:  []plugin.CageEntry{{Family: "http", Target: "x.com", Access: []string{"read"}}},
	}
}

func TestManifest_Validate_FailClosed(t *testing.T) {
	if err := validManifest().Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	bad := map[string]func(*plugin.Manifest){
		"empty name":        func(m *plugin.Manifest) { m.Name = "" },
		"spaced name":       func(m *plugin.Manifest) { m.Name = "Bad Name" },
		"no version":        func(m *plugin.Manifest) { m.Version = "" },
		"no tools":          func(m *plugin.Manifest) { m.Tools = nil },
		"dup tool":          func(m *plugin.Manifest) { m.Tools = append(m.Tools, m.Tools[0]) },
		"non-obj params":    func(m *plugin.Manifest) { m.Tools[0].Parameters = []byte(`"nope"`) },
		"empty cage family": func(m *plugin.Manifest) { m.Cage[0].Family = "" },
		"empty cage target": func(m *plugin.Manifest) { m.Cage[0].Target = "" },
		"empty cage access": func(m *plugin.Manifest) { m.Cage[0].Access = nil },
		"bad cage access":   func(m *plugin.Manifest) { m.Cage[0].Access = []string{"delete"} },
	}
	for name, mut := range bad {
		m := validManifest()
		mut(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

// oauthManifest is a valid manifest whose oauth block links to a matching
// credential — the shape a Gmail-style plugin uses.
func oauthManifest() plugin.Manifest {
	m := validManifest()
	m.Cage = append(m.Cage, plugin.CageEntry{Family: "http", Target: "x.com", Access: []string{"read", "write"}})
	m.Credentials = []plugin.CredentialDecl{{Name: "acct", Family: "http", Host: "x.com", Header: "Authorization"}}
	m.OAuth = []plugin.OAuthDecl{{
		Name: "acct", ClientID: "cid",
		AuthURL: "https://auth.example.com/a", TokenURL: "https://token.example.com/t",
		Scopes: []string{"read"},
	}}
	return m
}

func TestManifest_OAuthValidation(t *testing.T) {
	if err := oauthManifest().Validate(); err != nil {
		t.Fatalf("valid oauth manifest rejected: %v", err)
	}
	bad := map[string]func(*plugin.Manifest){
		"no client_id":       func(m *plugin.Manifest) { m.OAuth[0].ClientID = "" },
		"no scopes":          func(m *plugin.Manifest) { m.OAuth[0].Scopes = nil },
		"http auth_url":      func(m *plugin.Manifest) { m.OAuth[0].AuthURL = "http://auth.example.com/a" },
		"empty token_url":    func(m *plugin.Manifest) { m.OAuth[0].TokenURL = "" },
		"no matching cred":   func(m *plugin.Manifest) { m.OAuth[0].Name = "orphan" },
		"cred name mismatch": func(m *plugin.Manifest) { m.Credentials[0].Name = "other" },
	}
	for name, mut := range bad {
		m := oauthManifest()
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
	intent  string // the prompt text of the most recent Notify
}

func (n *countNotifier) Notify(intent string, options []hitl.Option) error {
	n.calls++
	n.intent = intent
	for _, o := range options {
		if o.Outcome == n.want {
			return n.resolve(o.Token)
		}
	}
	return errors.New("countNotifier: no matching option")
}

// End to end through the real QuickJS interpreter: a plugin's effect inside its
// cage asks once then goes silent after a session grant; an effect OUTSIDE the
// cage is hard-denied without ever asking; uninstall removes the tools.
func TestPlugin_CageBoundsEffects_E2E(t *testing.T) {
	stub := &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
	})}
	notifier := &countNotifier{want: hitl.ApprovedSession}
	engine := hitl.NewEngine([]byte("k"), notifier)
	notifier.resolve = engine.Resolve
	guard := &gateway.Guard{
		Policy: capability.Policy{Rules: []capability.Rule{
			{Family: "http", TargetGlob: capability.Wildcard, Writes: capability.MatchAny, Effect: capability.Ask, Epoch: capability.Permanent},
		}},
		Approvals: engine,
		TTL:       time.Second,
	}
	netCap := netcap.New(guard, netcap.WithHTTPClient(stub), netcap.WithScanner(secret.NewScanner(secret.NewStore())))
	reg := tool.NewRegistry().AddMany(netCap.Tools()...)

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
	ctx := capability.WithGrants(context.Background(), capability.NewGrants(capability.Permanent, nil))

	// 1. in-cage fetch → asks once → session grant → 2nd is silent.
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

	// 2. out-of-cage evil → hard denied, human NEVER asked; tool surfaces the error.
	before := notifier.calls
	if _, err := reg.Invoke(ctx, "example.evil", "{}"); err == nil {
		t.Fatal("evil (host outside the cage) must fail")
	}
	if notifier.calls != before {
		t.Fatalf("out-of-cage call asked the human (%d→%d), want 0 extra asks", before, notifier.calls)
	}

	// 3. uninstall removes the tools.
	if err := host.Uninstall("example"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if reg.Has("example.fetch") {
		t.Fatal("uninstall did not remove example.fetch")
	}
}

// Semantic HITL wording: a plugin tool with a manifest intent template makes the
// human see "Send hi to the example API" — rendered from the tool's args — rather
// than the transport-level "http.write api.example.com" the effect performs.
func TestPlugin_ManifestIntentReachesHITL(t *testing.T) {
	stub := &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
	})}
	notifier := &countNotifier{want: hitl.Approved}
	engine := hitl.NewEngine([]byte("k"), notifier)
	notifier.resolve = engine.Resolve
	guard := &gateway.Guard{
		Policy: capability.Policy{Rules: []capability.Rule{
			{Family: "http", TargetGlob: capability.Wildcard, Writes: capability.MatchAny, Effect: capability.Ask, Epoch: capability.Permanent},
		}},
		Approvals: engine,
		TTL:       time.Second,
	}
	netCap := netcap.New(guard, netcap.WithHTTPClient(stub), netcap.WithScanner(secret.NewScanner(secret.NewStore())))
	reg := tool.NewRegistry().AddMany(netCap.Tools()...)

	host := plugin.NewHost(reg, nil)
	l, err := plugin.Load("testdata/example")
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Install(l, func(plugin.Manifest) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("install: %v", err)
	}
	ctx := capability.WithGrants(context.Background(), capability.NewGrants(capability.Permanent, nil))

	if _, err := reg.Invoke(ctx, "example.send", `{"msg":"hi"}`); err != nil {
		t.Fatalf("send: %v", err)
	}
	// The prompt head is the rendered manifest template; the fact line beneath names
	// the OUTERMOST model-facing tool (example.send) — not the inner http.write it
	// reaches — proving outermost-wins for the grant/display tool name.
	head, fact, ok := strings.Cut(notifier.intent, "\n")
	if head != "Send hi to the example API" {
		t.Fatalf("HITL prompt head = %q, want the rendered manifest template", head)
	}
	if !ok || !strings.Contains(fact, "example.send") {
		t.Fatalf("fact line = %q, want it to name the outermost tool example.send", fact)
	}
}

type staticSource string

func (s staticSource) Value(context.Context) ([]byte, error) { return []byte(s), nil }

// Exfil closure: an attacker plugin cannot reach another plugin's credential by
// re-using its credential NAME. Credentials are namespaced plugin:<name>/<cred>,
// so the attacker's binding resolves only its OWN (missing) source — never the
// victim's token — and owner-scoped injection blocks its calls from matching the
// victim's binding at all.
func TestHost_CredentialsPluginNamespaced_NoExfil(t *testing.T) {
	store := secret.NewStore()
	inj := secret.NewInjector(store)
	host := plugin.NewHost(tool.NewRegistry(), inj)
	approve := func(plugin.Manifest) (bool, error) { return true, nil }

	loaded := func(name, dest string) plugin.Loaded {
		return plugin.Loaded{Kind: plugin.KindJS, Artifact: []byte("//x"), Manifest: plugin.Manifest{
			Name: name, Version: "1",
			Tools:       []plugin.ToolDecl{{Name: "t", Parameters: []byte(`{"type":"object"}`)}},
			Cage:        []plugin.CageEntry{{Family: "http", Target: dest, Access: []string{"read"}}},
			Credentials: []plugin.CredentialDecl{{Name: "tok", Family: "http", Host: dest, Header: "Authorization", Prefix: "Bearer "}},
		}}
	}

	if err := host.Install(loaded("victim", "api.example.com"), approve); err != nil {
		t.Fatal(err)
	}
	// The victim's OAuth wiring registers its source under the host-bound key.
	inj.SetResolver(plugin.SecretName(plugin.Owner("victim"), "tok", "api.example.com"), staticSource("VICTIM-TOKEN"))

	// The attacker declares the SAME credential name "tok", pointed at its own host.
	if err := host.Install(loaded("attacker", "attacker.example.com"), approve); err != nil {
		t.Fatal(err)
	}

	req := &secret.Request{}
	_, err := inj.InjectMatching(secret.WithOwner(context.Background(), plugin.Owner("attacker")), req, "attacker.example.com")
	if got := req.Headers["Authorization"]; got == "Bearer VICTIM-TOKEN" {
		t.Fatal("EXFIL: attacker resolved the victim's token via a shared credential name")
	}
	if err == nil {
		t.Fatalf("attacker resolved SOME credential it should not have: %v", req.Headers)
	}
}

// Host-rebind exfil regression: a plugin credential is keyed by (owner, cred,
// HOST). A token stored for host A must not be reachable when the SAME plugin's
// SAME credential is repointed at host B — the host-B binding resolves a
// different key, finds nothing, and fails closed. So editing a manifest to
// repoint a credential can never silently reuse the host-A token against host B.
func TestHost_CredentialHostBound_NoCrossHostReuse(t *testing.T) {
	store := secret.NewStore()
	// The real token, issued for host A.
	store.Set(plugin.SecretName(plugin.Owner("gmail"), "tok", "api.example.com"), []byte("TOKEN-A"))
	inj := secret.NewInjector(store)
	host := plugin.NewHost(tool.NewRegistry(), inj)
	approve := func(plugin.Manifest) (bool, error) { return true, nil }

	// Same plugin name, same credential name, repointed to host B.
	repointed := plugin.Loaded{Kind: plugin.KindJS, Artifact: []byte("//x"), Manifest: plugin.Manifest{
		Name: "gmail", Version: "1",
		Tools:       []plugin.ToolDecl{{Name: "t", Parameters: []byte(`{"type":"object"}`)}},
		Cage:        []plugin.CageEntry{{Family: "http", Target: "evil.example.com", Access: []string{"read"}}},
		Credentials: []plugin.CredentialDecl{{Name: "tok", Family: "http", Host: "evil.example.com", Header: "Authorization", Prefix: "Bearer "}},
	}}
	if err := host.Install(repointed, approve); err != nil {
		t.Fatal(err)
	}

	req := &secret.Request{}
	_, err := inj.InjectMatching(secret.WithOwner(context.Background(), plugin.Owner("gmail")), req, "evil.example.com")
	if got := req.Headers["Authorization"]; got == "Bearer TOKEN-A" {
		t.Fatal("EXFIL: the host-A token was injected to host B via a repointed credential")
	}
	if err == nil {
		t.Fatalf("host-B binding resolved a credential it should not have: %v", req.Headers)
	}
}

// The runWASM path end to end: a KindWASM plugin feeds {"tool","args"} on stdin to
// a real wasm32-wasi guest, which forwards it to the one gate; the dispatcher
// routes to the shared registry and the result flows back on stdout. First
// coverage of the plugin.wasm contract (distinct from the plugin.js path).
func TestPlugin_WASM_DispatchesThroughGate_E2E(t *testing.T) {
	l, err := plugin.Load("testdata/wasmprobe")
	if err != nil {
		t.Fatal(err)
	}
	if l.Kind != plugin.KindWASM {
		t.Fatalf("kind = %v, want KindWASM", l.Kind)
	}

	// The wasmprobe guest forwards {tool:"ping",args} verbatim to nocturn.call, so
	// the gate dispatches to an effect tool literally named "ping".
	var gotArgs string
	ping := tool.Tool{
		Spec: tool.Spec{Name: "ping"},
		Invoke: func(_ context.Context, args string) (string, error) {
			gotArgs = args
			return "pong", nil
		},
	}
	reg := tool.NewRegistry().AddMany([]tool.Tool{ping}...)
	p := plugin.New(l, reg)

	var pluginTool tool.Tool
	for _, tl := range p.Tools() {
		if tl.Name == "wasmprobe.ping" {
			pluginTool = tl
		}
	}
	if pluginTool.Name == "" {
		t.Fatal("wasmprobe.ping not exposed by the plugin")
	}

	out, err := pluginTool.Invoke(context.Background(), `{"n":7}`)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if strings.TrimSpace(out) != "pong" {
		t.Fatalf("out = %q, want pong", out)
	}
	if gotArgs != `{"n":7}` {
		t.Fatalf("effect tool received args %q, want %q", gotArgs, `{"n":7}`)
	}
}
