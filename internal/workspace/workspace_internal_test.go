package workspace

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/memory"
	"github.com/efuturetoday/nocturn/internal/plugin"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/tools"
)

// llmStub satisfies agentkit.LLM for AgentTool construction; it is never actually called here.
type llmStub struct{}

func (llmStub) Next(context.Context, []agentkit.Message, []agentkit.ToolSpec) (agentkit.Step, error) {
	return agentkit.Step{}, nil
}

// captureHandler records emitted slog records so a test can assert a warning fired.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r.Clone())
	h.mu.Unlock()
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) sawLevel(l slog.Level) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Level == l {
			return true
		}
	}
	return false
}

func mustTool(t *testing.T, name string) agentkit.Tool {
	t.Helper()
	tool, err := agentkit.NewTool(name, "test tool", func(context.Context, string) (string, error) { return "", nil })
	if err != nil {
		t.Fatalf("NewTool %q: %v", name, err)
	}
	return tool
}

// TestBuildTools_IncludesAgentToolPerDeclaredAgent: buildTools composes the root cage (base tools +
// code_run) and layers one AgentTool per declared agent on top.
func TestBuildTools_IncludesAgentToolPerDeclaredAgent(t *testing.T) {
	base, err := agentkit.NewToolSet(mustTool(t, "alpha"), mustTool(t, "beta"))
	if err != nil {
		t.Fatalf("base toolset: %v", err)
	}
	agents := agent.Set{
		"scout":   {Name: "scout", Tools: []string{"alpha"}},
		"planner": {Name: "planner"}, // pure reasoner
	}

	set, err := buildTools(base, llmStub{}, agents, nil)
	if err != nil {
		t.Fatalf("buildTools: %v", err)
	}

	// Root cage: the base tools plus code_run woven in by Compose.
	for _, want := range []string{"alpha", "beta", tools.CodeRunTool} {
		if _, ok := set[want]; !ok {
			t.Errorf("root cage missing %q", want)
		}
	}
	// One AgentTool per declared agent, named after the agent.
	for _, want := range []string{"scout", "planner"} {
		if _, ok := set[want]; !ok {
			t.Errorf("missing AgentTool for declared agent %q", want)
		}
	}
	// base(2) + code_run(1) + agents(2) = 5, and no stray tools.
	if len(set) != 5 {
		t.Errorf("toolset size = %d, want 5 (2 base + code_run + 2 agents)", len(set))
	}
}

// TestResolvePersona_PersonaMdOverride_ElseDefault: a non-empty PERSONA.md becomes the system prompt;
// its absence or emptiness falls back to the built-in default.
//
// NOTE (discrepancy vs plan name "LayeredElseDefault"): resolvePersona does not LAYER the override
// over the default — a present, non-empty PERSONA.md REPLACES the default entirely. These assert the
// actual replace-else-default behavior.
func TestResolvePersona_PersonaMdOverride_ElseDefault(t *testing.T) {
	log := slog.New(slog.DiscardHandler)

	t.Run("no PERSONA.md uses default", func(t *testing.T) {
		if got := resolvePersona(t.TempDir(), log); got != defaultPersona {
			t.Errorf("resolvePersona = %q, want default", got)
		}
	})

	t.Run("override replaces default", func(t *testing.T) {
		dir := t.TempDir()
		const persona = "You are Vega, an astronomy tutor."
		if err := os.WriteFile(filepath.Join(dir, "PERSONA.md"), []byte("  "+persona+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := resolvePersona(dir, log); got != persona {
			t.Errorf("resolvePersona = %q, want the (trimmed) override %q", got, persona)
		}
	})

	t.Run("empty PERSONA.md uses default", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "PERSONA.md"), []byte("   \n\t"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := resolvePersona(dir, log); got != defaultPersona {
			t.Errorf("resolvePersona = %q, want default for an empty override", got)
		}
	})
}

// TestResolvePersona_ReadError_WarnsAndDefaults: a real read error on PERSONA.md (here: it is a
// directory) must NOT silently swap identity — it warns and returns the default.
func TestResolvePersona_ReadError_WarnsAndDefaults(t *testing.T) {
	dir := t.TempDir()
	// A directory named PERSONA.md makes os.ReadFile fail with something other than ErrNotExist.
	if err := os.Mkdir(filepath.Join(dir, "PERSONA.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	h := &captureHandler{}
	got := resolvePersona(dir, slog.New(h))
	if got != defaultPersona {
		t.Errorf("resolvePersona = %q, want default on read error (identity must not be swapped)", got)
	}
	if !h.sawLevel(slog.LevelWarn) {
		t.Error("a read error on PERSONA.md must surface a warning, not pass silently")
	}
}

// TestPolicy_NetAndFileAsk_ElseAllowed: the workspace policy asks (session recall) on the net and
// file kinds and allows everything else freely.
func TestPolicy_NetAndFileAsk_ElseAllowed(t *testing.T) {
	p := policy()
	ask := gate.AskWith(gate.RecallSession)
	allow := gate.Allowed()

	tests := []struct {
		name string
		kind string
		want gate.Ruling
	}{
		{"net asks", tools.NetKind, ask},
		{"file asks", tools.FileKind, ask},
		{"time allowed", "time_now", allow},
		{"notify allowed", tools.NotifyKind, allow},
		{"unknown allowed", "whatever", allow},
		// A watched chat writes memory without a prompt: the call is already visible in the transcript
		// as it happens, so asking would buy "before" instead of "after" and nothing else.
		{"memory allowed in a watched chat", memory.Kind, allow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.Decide(gate.Action{Kind: tt.kind}); got != tt.want {
				t.Errorf("Decide(%q) ruling mismatch", tt.kind)
			}
		})
	}
}

// TestAgentPolicy_AsksOnMemory: an unattended run writes into the store folded into EVERY future
// prompt with nobody reading its transcript, so memory — and only memory — is tightened for agents.
// Everything else must stay exactly as the root policy rules it, or the two would drift.
func TestAgentPolicy_AsksOnMemory(t *testing.T) {
	agentP, rootP := agentPolicy(), policy()

	if got, want := agentP.Decide(gate.Action{Kind: memory.Kind}), gate.AskWith(gate.RecallSession); got != want {
		t.Errorf("agent policy on memory = %+v, want ask", got)
	}
	for _, kind := range []string{tools.NetKind, tools.FileKind, tools.NotifyKind, tools.RemindKind, "time_now", "whatever"} {
		if got, want := agentP.Decide(gate.Action{Kind: kind}), rootP.Decide(gate.Action{Kind: kind}); got != want {
			t.Errorf("agent policy diverges from the root policy on %q", kind)
		}
	}
}

// writePlugin lays down a valid JS plugin (plugin.json + plugin.js) under root/plugins/<name>.
func writePlugin(t *testing.T, root, name, tool string, creds []secret.Binding) {
	t.Helper()
	pdir := filepath.Join(root, "plugins", name)
	if err := os.MkdirAll(pdir, 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString(`{"name":"` + name + `","version":"1.0.0",`)
	b.WriteString(`"tools":[{"name":"` + tool + `","description":"x","parameters":{"type":"object"}}]`)
	if len(creds) > 0 {
		b.WriteString(`,"credentials":[`)
		for i, c := range creds {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"name":"` + c.Secret + `","host":"` + c.Host + `","header":"` + c.Header + `","prefix":"` + c.Prefix + `"}`)
		}
		b.WriteString(`]`)
	}
	b.WriteString(`}`)
	if err := os.WriteFile(filepath.Join(pdir, "plugin.json"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, "plugin.js"), []byte("globalThis.plugin = {tools:{}};\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestInstallPlugins_NameCollision_Refused: a plugin tool whose namespaced name collides with an
// existing tool is refused fail-closed, not silently overwritten.
func TestInstallPlugins_NameCollision_Refused(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "dup", "greet", nil) // exposes tool "dup_greet"

	base, err := agentkit.NewToolSet()
	if err != nil {
		t.Fatal(err)
	}
	// Pre-seed the workspace toolset with the name the plugin will try to claim.
	toolset, err := agentkit.NewToolSet(mustTool(t, "dup_greet"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := installPlugins(dir, base, toolset, nil, nil); err == nil {
		t.Fatal("installPlugins must refuse a plugin tool that collides with an existing tool")
	}
}

// TestInstallPlugins_BindsCredentialsUnderOwner: a plugin's declared credential is bound on the
// injector under the plugin's owner — so it rides the plugin's own calls but not a bare model call.
func TestInstallPlugins_BindsCredentialsUnderOwner(t *testing.T) {
	dir := t.TempDir()
	cred := secret.Binding{Secret: "mytoken", Host: "api.example.com", Header: "Authorization", Prefix: "Bearer "}
	writePlugin(t, dir, "creds", "noop", []secret.Binding{cred})

	store := secret.NewStore()
	// installPlugins binds the credential under the owner-namespaced key plugin.SecretName(plugin,
	// cred), so the stored value must live there too (not the bare credential name).
	store.Set(plugin.SecretName("creds", "mytoken"), []byte("s3cr3t"))
	inj := secret.NewInjector(store)

	base, err := agentkit.NewToolSet()
	if err != nil {
		t.Fatal(err)
	}
	toolset, err := agentkit.NewToolSet()
	if err != nil {
		t.Fatal(err)
	}
	if n, err := installPlugins(dir, base, toolset, inj, nil); err != nil || n != 1 {
		t.Fatalf("installPlugins = %d, %v; want 1, nil", n, err)
	}

	// As the plugin owner, the credential is injected at the bound host.
	owned := &secret.Request{Method: "GET", URL: "https://api.example.com/x"}
	names, err := inj.InjectMatching(secret.WithOwner(context.Background(), "plugin:creds"), owned, "api.example.com")
	if err != nil {
		t.Fatalf("InjectMatching (owner): %v", err)
	}
	if got := owned.Headers["Authorization"]; got != "Bearer s3cr3t" {
		t.Errorf("owner request Authorization = %q, want %q; injected=%v", got, "Bearer s3cr3t", names)
	}

	// A bare model call (no owner) must NOT pick up the plugin's owner-scoped credential.
	bare := &secret.Request{Method: "GET", URL: "https://api.example.com/x"}
	if _, err := inj.InjectMatching(context.Background(), bare, "api.example.com"); err != nil {
		t.Fatalf("InjectMatching (no owner): %v", err)
	}
	if _, ok := bare.Headers["Authorization"]; ok {
		t.Error("plugin credential leaked onto an unowned call — it must stay owner-scoped")
	}
}

// memoryWith lays one note into a fresh memory folder and returns a Store over it. summary empty
// means an empty memory.
func memoryWith(t *testing.T, note, summary string) *memory.Store {
	t.Helper()
	dir := t.TempDir()
	if summary != "" {
		if err := os.WriteFile(filepath.Join(dir, note), []byte("---\ndescription: "+summary+"\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return memory.New(dir, nil)
}

func hasAll(names ...string) func(string) bool {
	set := make(map[string]struct{}, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}
	return func(n string) bool { _, ok := set[n]; return ok }
}

// TestComposePrompt_FoldsMemoryIntoTheBase: the live index rides along with the persona, so the
// model knows its user without spending a tool call.
func TestComposePrompt_FoldsMemoryIntoTheBase(t *testing.T) {
	mem := memoryWith(t, "lina.md", "daughter, 7 years old")

	got := composePrompt("PERSONA", mem, hasAll("memory_read", "memory_write"))
	if !strings.HasPrefix(got, "PERSONA") {
		t.Fatalf("prompt does not start with the base identity: %q", got)
	}
	if !strings.Contains(got, "<memory") || !strings.Contains(got, "lina.md — daughter, 7 years old") {
		t.Fatalf("prompt is missing the memory block: %q", got)
	}
}

// TestComposePrompt_OmittedWhenNothingToSay: a fresh workspace must cost zero prompt tokens, and a
// runner whose cage has no memory tool must not be handed the user's notes.
func TestComposePrompt_OmittedWhenNothingToSay(t *testing.T) {
	filled := memoryWith(t, "lina.md", "daughter, 7 years old")

	for _, tc := range []struct {
		name string
		mem  *memory.Store
		has  func(string) bool
	}{
		{"empty memory", memoryWith(t, "lina.md", ""), hasAll("memory_read", "memory_write")},
		{"no memory tool in the cage", filled, hasAll("file_read")},
		{"no store at all", nil, hasAll("memory_read")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := composePrompt("PERSONA", tc.mem, tc.has); got != "PERSONA" {
				t.Fatalf("prompt = %q, want the bare base", got)
			}
		})
	}
}

// TestComposePrompt_ReadOnlyCageStillSeesTheIndex: memory_read alone is enough to be shown the
// notes — an agent that may consult but not amend them is a legitimate configuration.
func TestComposePrompt_ReadOnlyCageStillSeesTheIndex(t *testing.T) {
	mem := memoryWith(t, "coding.md", "Go, no comments")
	if got := composePrompt("A", mem, hasAll("memory_read")); !strings.Contains(got, "coding.md — Go, no comments") {
		t.Fatalf("read-only cage got no memory block: %q", got)
	}
}
