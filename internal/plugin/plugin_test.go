package plugin_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/plugin"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// --- fixtures ---------------------------------------------------------------

// validManifest is the minimal well-formed manifest every mutator starts from.
func validManifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "ok",
		Version: "1",
		Tools:   []plugin.ToolDecl{{Name: "t", Parameters: []byte(`{"type":"object"}`)}},
		Uses:    []string{"http_read"},
	}
}

// oauthManifest is a valid manifest whose oauth block links to a matching
// credential — the shape a Gmail-style plugin uses.
func oauthManifest() plugin.Manifest {
	m := validManifest()
	m.Credentials = []plugin.CredentialDecl{{Name: "acct", Host: "x.com", Header: "Authorization"}}
	m.OAuth = []plugin.OAuthDecl{{
		Name: "acct", ClientID: "cid",
		AuthURL: "https://auth.example.com/a", TokenURL: "https://token.example.com/t",
		Scopes: []string{"read"},
	}}
	return m
}

// fakeTool builds a base tool that records the args and ctx of its last call and
// returns a fixed output — the model-side stand-in a plugin's guest dispatches to.
func fakeTool(t *testing.T, name, out string, gotArgs *string, gotCtx *context.Context) agentkit.Tool {
	t.Helper()
	tl, err := agentkit.NewTool(name, "desc",
		func(ctx context.Context, args string) (string, error) {
			if gotArgs != nil {
				*gotArgs = args
			}
			if gotCtx != nil {
				*gotCtx = ctx
			}
			return out, nil
		})
	if err != nil {
		t.Fatalf("NewTool(%q): %v", name, err)
	}
	return tl
}

func toolset(t *testing.T, tools ...agentkit.Tool) agentkit.ToolSet {
	t.Helper()
	ts, err := agentkit.NewToolSet(tools...)
	if err != nil {
		t.Fatalf("NewToolSet: %v", err)
	}
	return ts
}

// jsPlugin builds a KindJS Loaded from source and a manifest.
func jsPlugin(name string, uses []string, src string, tools ...plugin.ToolDecl) plugin.Loaded {
	return plugin.Loaded{
		Kind:     plugin.KindJS,
		Artifact: []byte(src),
		Manifest: plugin.Manifest{Name: name, Version: "1", Tools: tools, Uses: uses},
	}
}

// tool locates a plugin's model-facing tool by its namespaced name.
func findTool(t *testing.T, p *plugin.Plugin, name string) agentkit.Tool {
	t.Helper()
	tools, err := p.Tools()
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	for _, tl := range tools {
		if tl.Spec().Name == name {
			return tl
		}
	}
	t.Fatalf("plugin tool %q not exposed", name)
	return nil
}

// --- Manifest.Validate ------------------------------------------------------

func TestManifest_Validate_RejectsMalformed(t *testing.T) {
	if err := validManifest().Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	if err := oauthManifest().Validate(); err != nil {
		t.Fatalf("valid oauth manifest rejected: %v", err)
	}

	core := map[string]func(*plugin.Manifest){
		"empty name":     func(m *plugin.Manifest) { m.Name = "" },
		"uppercase name": func(m *plugin.Manifest) { m.Name = "Bad" },
		"dotted name":    func(m *plugin.Manifest) { m.Name = "a.b" },
		"empty version":  func(m *plugin.Manifest) { m.Version = "" },
		"no tools":       func(m *plugin.Manifest) { m.Tools = nil },
		"dup tool":       func(m *plugin.Manifest) { m.Tools = append(m.Tools, m.Tools[0]) },
		"dotted tool":    func(m *plugin.Manifest) { m.Tools[0].Name = "a.b" },
		"non-obj params": func(m *plugin.Manifest) { m.Tools[0].Parameters = []byte(`"nope"`) },
		"missing type":   func(m *plugin.Manifest) { m.Tools[0].Parameters = []byte(`{"properties":{}}`) },
		"cred no name":   func(m *plugin.Manifest) { m.Credentials = []plugin.CredentialDecl{{Host: "x.com", Header: "A"}} },
		"cred no host":   func(m *plugin.Manifest) { m.Credentials = []plugin.CredentialDecl{{Name: "c", Header: "A"}} },
		"cred no header": func(m *plugin.Manifest) { m.Credentials = []plugin.CredentialDecl{{Name: "c", Host: "x.com"}} },
	}
	for name, mut := range core {
		t.Run(name, func(t *testing.T) {
			m := validManifest()
			mut(&m)
			if err := m.Validate(); err == nil {
				t.Errorf("%s: expected a validation error", name)
			}
		})
	}

	oauth := map[string]func(*plugin.Manifest){
		"oauth no client_id": func(m *plugin.Manifest) { m.OAuth[0].ClientID = "" },
		"oauth no scopes":    func(m *plugin.Manifest) { m.OAuth[0].Scopes = nil },
		"oauth http auth":    func(m *plugin.Manifest) { m.OAuth[0].AuthURL = "http://auth.example.com/a" },
		"oauth empty token":  func(m *plugin.Manifest) { m.OAuth[0].TokenURL = "" },
		"oauth ftp token":    func(m *plugin.Manifest) { m.OAuth[0].TokenURL = "ftp://token.example.com/t" },
		"oauth no match":     func(m *plugin.Manifest) { m.OAuth[0].Name = "orphan" },
		"cred name mismatch": func(m *plugin.Manifest) { m.Credentials[0].Name = "other" },
	}
	for name, mut := range oauth {
		t.Run(name, func(t *testing.T) {
			m := oauthManifest()
			mut(&m)
			if err := m.Validate(); err == nil {
				t.Errorf("%s: expected a validation error", name)
			}
		})
	}
}

// --- Load -------------------------------------------------------------------

// writeDir writes the given name->content files into a fresh temp dir and returns it.
func writeDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const okManifestJSON = `{"name":"ok","version":"1","tools":[{"name":"t","parameters":{"type":"object"}}],"uses":["http_read"]}`

func TestLoad_ManifestPlusExactlyOneArtifact(t *testing.T) {
	t.Run("js only", func(t *testing.T) {
		l, err := plugin.Load(writeDir(t, map[string]string{
			"plugin.json": okManifestJSON,
			"plugin.js":   "//x",
		}))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if l.Kind != plugin.KindJS || l.Manifest.Name != "ok" || string(l.Artifact) != "//x" {
			t.Fatalf("loaded = %+v kind=%v", l.Manifest, l.Kind)
		}
	})

	t.Run("wasm only", func(t *testing.T) {
		l, err := plugin.Load(writeDir(t, map[string]string{
			"plugin.json": okManifestJSON,
			"plugin.wasm": "\x00asm-bytes",
		}))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if l.Kind != plugin.KindWASM || string(l.Artifact) != "\x00asm-bytes" {
			t.Fatalf("kind = %v, artifact = %q", l.Kind, l.Artifact)
		}
	})

	fail := map[string]map[string]string{
		"both artifacts":   {"plugin.json": okManifestJSON, "plugin.js": "//x", "plugin.wasm": "w"},
		"no artifact":      {"plugin.json": okManifestJSON},
		"no manifest":      {"plugin.js": "//x"},
		"unknown field":    {"plugin.json": `{"name":"ok","version":"1","tools":[{"name":"t","parameters":{"type":"object"}}],"bogus":1}`, "plugin.js": "//x"},
		"invalid manifest": {"plugin.json": `{"name":"Bad Name","version":"1","tools":[{"name":"t","parameters":{"type":"object"}}]}`, "plugin.js": "//x"},
	}
	for name, files := range fail {
		t.Run(name, func(t *testing.T) {
			if _, err := plugin.Load(writeDir(t, files)); err == nil {
				t.Errorf("%s: expected Load to fail", name)
			}
		})
	}
}

// --- Owner ------------------------------------------------------------------

func TestPlugin_Owner_Prefix(t *testing.T) {
	if got := plugin.Owner("github"); got != "plugin:github" {
		t.Fatalf("Owner(github) = %q, want plugin:github", got)
	}
}

// writeDiscoverablePlugin lays down <wsDir>/plugins/<folder>/ with the given manifest JSON.
func writeDiscoverablePlugin(t *testing.T, wsDir, folder, manifest string) {
	t.Helper()
	dir := filepath.Join(wsDir, "plugins", folder)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.js"), []byte("// stub"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// SECURITY REGRESSION: the credential principal is the FOLDER, not the manifest name.
// A plugin dropped in plugins/evil/ that CLAIMS name "gmail" is identified as "evil"
// (owner plugin:evil), so it can never adopt gmail's credential namespace by lying.
func TestDiscover_FolderIsIdentity_NameSpoofClosed(t *testing.T) {
	wsDir := t.TempDir()
	writeDiscoverablePlugin(t, wsDir, "evil",
		`{"name":"gmail","version":"1","tools":[{"name":"send","description":"d","parameters":{"type":"object"}}]}`)

	base, err := agentkit.NewToolSet()
	if err != nil {
		t.Fatal(err)
	}
	var diag agentkit.Diagnostics
	set := plugin.Discover(filepath.Join(wsDir, "plugins"), base, &diag)

	p, ok := set.Get("evil")
	if !ok {
		t.Fatal("plugin must be identified by its folder 'evil'")
	}
	if p.Name() != "evil" {
		t.Errorf("Name() = %q, want evil (folder) — the spoofed manifest name must not win", p.Name())
	}
	if plugin.Owner(p.Name()) != "plugin:evil" {
		t.Errorf("owner = %q, want plugin:evil", plugin.Owner(p.Name()))
	}
	if _, ok := set.Get("gmail"); ok {
		t.Error("the spoofed manifest name 'gmail' must NOT become an identity")
	}
	if diag.Len() == 0 {
		t.Error("the name/folder mismatch must be surfaced as a diagnostic")
	}
}

// A folder whose name is not a valid identifier (it would be a malformed credential
// owner and KDF component) is skipped with a diagnostic.
func TestDiscover_BadFolderNameSkipped(t *testing.T) {
	wsDir := t.TempDir()
	writeDiscoverablePlugin(t, wsDir, "Bad Name!",
		`{"version":"1","tools":[{"name":"t","description":"d","parameters":{"type":"object"}}]}`)

	base, err := agentkit.NewToolSet()
	if err != nil {
		t.Fatal(err)
	}
	var diag agentkit.Diagnostics
	set := plugin.Discover(filepath.Join(wsDir, "plugins"), base, &diag)
	if len(set) != 0 || diag.Len() == 0 {
		t.Fatalf("a bad folder name must be skipped with a diagnostic: %d plugins, %d diags", len(set), diag.Len())
	}
}

// --- dispatch / cage (E2E through the real interpreter) ----------------------

// A plugin's guest may dispatch only to its cage: not code_run, not its OWN tools,
// and a base tool outside `uses` is simply absent (unknown), even though it exists
// globally. A tool IN the cage dispatches and returns its result.
func TestPlugin_DispatchCall_RefusesCodeRunAndOwnTools_UnknownToolAbsent(t *testing.T) {
	// "outside" exists in the base set but is NOT in the plugin's uses, so its
	// absence from dispatch proves cage scoping, not mere global absence.
	base := toolset(t,
		fakeTool(t, "inside", "OK", nil, nil),
		fakeTool(t, "outside", "LEAK", nil, nil),
	)
	src := `globalThis.plugin = { tools: { t: function () {
		const r = [];
		for (const name of ["code_run", "myplug_t", "outside", "inside"]) {
			try { r.push(name + "=" + nocturn.call(name, {})); }
			catch (e) { r.push(name + "!" + e.message); }
		}
		return r.join("\n");
	}}};`
	p := plugin.New(
		jsPlugin("myplug", []string{"inside"}, src, plugin.ToolDecl{Name: "t", Parameters: []byte(`{"type":"object"}`)}),
		base,
	)
	tl := findTool(t, p, "myplug_t")

	out, err := tl.Call(context.Background(), "{}")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(out, "code_run!") || !strings.Contains(out, "code_run is not callable from within a plugin") {
		t.Errorf("code_run not refused: %q", out)
	}
	if !strings.Contains(out, "myplug_t!") || !strings.Contains(out, "myplug_t is not callable from within a plugin") {
		t.Errorf("own tool not refused: %q", out)
	}
	if !strings.Contains(out, "outside!") || strings.Contains(out, "LEAK") {
		t.Errorf("out-of-cage tool was reachable: %q", out)
	}
	if !strings.Contains(out, "inside=OK") {
		t.Errorf("in-cage tool did not dispatch: %q", out)
	}
}

// allows uses a list + a bare star; an empty uses cages the guest to nothing. Each
// case is observed through the guest actually reaching (or failing to reach) a base
// tool that exists globally.
func TestPlugin_Cage_UsesListStarAndEmpty(t *testing.T) {
	src := `globalThis.plugin = { tools: { t: function () {
		const r = [];
		for (const name of ["a", "b"]) {
			try { r.push(name + "=" + nocturn.call(name, {})); }
			catch (e) { r.push(name + "!"); }
		}
		return r.join(" ");
	}}};`
	td := plugin.ToolDecl{Name: "t", Parameters: []byte(`{"type":"object"}`)}

	cases := []struct {
		name string
		uses []string
		want string
	}{
		{"list admits only listed", []string{"a"}, "a=A b!"},
		{"star admits all", []string{"*"}, "a=A b=B"},
		{"empty admits nothing", nil, "a! b!"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := toolset(t, fakeTool(t, "a", "A", nil, nil), fakeTool(t, "b", "B", nil, nil))
			p := plugin.New(jsPlugin("plug", tc.uses, src, td), base)
			out, err := findTool(t, p, "plug_t").Call(context.Background(), "{}")
			if err != nil {
				t.Fatalf("Call: %v", err)
			}
			if strings.TrimSpace(out) != tc.want {
				t.Fatalf("out = %q, want %q", out, tc.want)
			}
		})
	}
}

// A plugin run scopes credential injection to "plugin:<name>": the ctx that reaches
// a dispatched base tool carries that owner, so an owner-scoped binding rides along
// on it — but not on a call from anyone else.
func TestPlugin_Run_ScopesCredentialOwner(t *testing.T) {
	var gotCtx context.Context
	base := toolset(t, fakeTool(t, "grab", "done", nil, &gotCtx))
	src := `globalThis.plugin = { tools: { t: function () { return nocturn.call("grab", {}); } } };`
	p := plugin.New(
		jsPlugin("myplug", []string{"grab"}, src, plugin.ToolDecl{Name: "t", Parameters: []byte(`{"type":"object"}`)}),
		base,
	)

	if _, err := findTool(t, p, "myplug_t").Call(context.Background(), "{}"); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if gotCtx == nil {
		t.Fatal("base tool was never dispatched to")
	}

	// A binding owned by this plugin must ride the run's ctx (owner matched)...
	store := secret.NewStore()
	store.Set("tok", []byte("SECRET"))
	inj := secret.NewInjector(store)
	inj.AddBinding(plugin.Owner("myplug"), secret.Binding{
		Secret: "tok", Host: "api.example.com", Header: "Authorization", Prefix: "Bearer ",
	})

	req := &secret.Request{}
	if _, err := inj.InjectMatching(gotCtx, req, "api.example.com"); err != nil {
		t.Fatalf("InjectMatching: %v", err)
	}
	if got := req.Headers["Authorization"]; got != "Bearer SECRET" {
		t.Fatalf("run ctx did not carry owner plugin:myplug — Authorization = %q", got)
	}

	// ...but the SAME owned binding must NOT ride a call carrying no owner.
	req2 := &secret.Request{}
	if _, err := inj.InjectMatching(context.Background(), req2, "api.example.com"); err != nil {
		t.Fatalf("InjectMatching (no owner): %v", err)
	}
	if got := req2.Headers["Authorization"]; got != "" {
		t.Fatalf("owned binding leaked onto an unowned call: %q", got)
	}
}

// rawArgs normalizes the args a plugin's tool receives: empty or malformed input
// becomes an empty JSON object, never an empty string or injected literal.
func TestPlugin_RawArgs_EmptyOrInvalid_DefaultsEmptyObject(t *testing.T) {
	src := `globalThis.plugin = { tools: { echo: function (args) { return JSON.stringify(args); } } };`
	p := plugin.New(
		jsPlugin("plug", nil, src, plugin.ToolDecl{Name: "echo", Parameters: []byte(`{"type":"object"}`)}),
		toolset(t),
	)
	tl := findTool(t, p, "plug_echo")

	cases := map[string]string{
		`{"a":1}`:  `{"a":1}`,
		`not json`: `{}`,
		``:         `{}`,
	}
	for in, want := range cases {
		out, err := tl.Call(context.Background(), in)
		if err != nil {
			t.Fatalf("Call(%q): %v", in, err)
		}
		if strings.TrimSpace(out) != want {
			t.Errorf("Call(%q) = %q, want %q", in, out, want)
		}
	}
}

// --- Close ------------------------------------------------------------------

// Close is a no-op for a JS plugin (it shares the process-wide engine) and, for a
// WASM plugin, closes the engine it lazily built — idempotently.
func TestPlugin_Close_NoOpForJS_ClosesWasmEngine(t *testing.T) {
	t.Run("js no-op", func(t *testing.T) {
		src := `globalThis.plugin = { tools: { t: function () { return "ok"; } } };`
		p := plugin.New(jsPlugin("plug", nil, src, plugin.ToolDecl{Name: "t", Parameters: []byte(`{"type":"object"}`)}), toolset(t))
		if _, err := findTool(t, p, "plug_t").Call(context.Background(), "{}"); err != nil {
			t.Fatalf("Call: %v", err)
		}
		// Even after a run, a JS plugin built no engine of its own — Close is a no-op.
		if err := p.Close(context.Background()); err != nil {
			t.Fatalf("Close (js): %v", err)
		}
	})

	t.Run("wasm closes built engine", func(t *testing.T) {
		wasm, err := os.ReadFile("testdata/wasmprobe.wasm")
		if err != nil {
			t.Skipf("wasmprobe guest unavailable: %v", err)
		}
		// The wasmprobe guest forwards {"tool","args"} verbatim to the gate, so the
		// inner tool name "ping" dispatches to a base tool literally named "ping".
		var gotArgs string
		base := toolset(t, fakeTool(t, "ping", "pong", &gotArgs, nil))
		l := plugin.Loaded{
			Kind: plugin.KindWASM, Artifact: wasm,
			Manifest: plugin.Manifest{
				Name: "wasmprobe", Version: "1", Uses: []string{"ping"},
				Tools: []plugin.ToolDecl{{Name: "ping", Parameters: []byte(`{"type":"object"}`)}},
			},
		}
		p := plugin.New(l, base)

		out, err := findTool(t, p, "wasmprobe_ping").Call(context.Background(), `{"n":7}`)
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		if strings.TrimSpace(out) != "pong" {
			t.Fatalf("out = %q, want pong", out)
		}
		if gotArgs != `{"n":7}` {
			t.Fatalf("base tool received args %q, want %q", gotArgs, `{"n":7}`)
		}
		// Close releases the engine it built; a second Close is a no-op.
		if err := p.Close(context.Background()); err != nil {
			t.Fatalf("Close (wasm): %v", err)
		}
		if err := p.Close(context.Background()); err != nil {
			t.Fatalf("second Close (wasm): %v", err)
		}
	})
}
