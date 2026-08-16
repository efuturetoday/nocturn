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
	"github.com/efuturetoday/nocturn/internal/knowledge/embed"
	"github.com/efuturetoday/nocturn/internal/mail"
	"github.com/efuturetoday/nocturn/internal/memory"
	"github.com/efuturetoday/nocturn/internal/plugin"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/speaker"
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

// TestPolicy_NetAndFileAsk_ElseAllowed: the workspace policy asks on the net and file kinds and
// allows everything else freely.
//
// The recall is a CEILING, not a decision — gate.Check takes min(ceiling, the human's choice). It is
// RecallAlways because both approvers offer an "always" button, and a lower ceiling would resolve
// that button to a session grant without saying so.
func TestPolicy_NetAndFileAsk_ElseAllowed(t *testing.T) {
	p := policy()
	ask := gate.AskWith(gate.RecallAlways)
	allow := gate.Allowed()

	tests := []struct {
		name string
		kind string
		want gate.Ruling
	}{
		{"net asks", tools.NetKind, ask},
		{"file asks", tools.FileKind, ask},
		// Sending mail asks even in a watched chat, unlike memory: the transcript shows it, but a
		// message that has reached a third party cannot be taken back afterwards.
		{"sending mail asks", mail.SendKind, ask},
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

	if got, want := agentP.Decide(gate.Action{Kind: memory.Kind}), gate.AskWith(gate.RecallAlways); got != want {
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

	if _, err := (pass{dir: dir}).installPlugins(base, toolset, nil); err == nil {
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
	if names, err := (pass{dir: dir, injector: inj}).installPlugins(base, toolset, nil); err != nil || len(names) != 1 {
		t.Fatalf("installPlugins = %v, %v; want one plugin, nil", names, err)
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

// whoami exists only where recognition does. Without a model loaded it could answer nothing but
// "unknown" for the life of the process, and a tool that is structurally incapable of a result
// costs a slot in every prompt while inviting a question whose answer is always no.
func TestOpen_WhoAmIOnlyWithASpeakerModel(t *testing.T) {
	has := func(t *testing.T, h Host) bool {
		t.Helper()
		w, err := Open(h, "test", t.TempDir())
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(w.Close)
		_, ok := w.snapshot().tools["whoami"]
		return ok
	}

	quiet := slog.New(slog.DiscardHandler)
	if has(t, Host{LLM: llmStub{}, Log: quiet}) {
		t.Error("whoami is registered with no speaker model — the terminal chat has no microphone at all")
	}
	// A non-nil embedder is the whole signal; Open never calls it.
	if !has(t, Host{LLM: llmStub{}, Speaker: &speaker.Embedder{}, Log: quiet}) {
		t.Error("whoami is missing although a speaker model is loaded")
	}
}

// knowledge_search exists only where an embedder does, the same rule as whoami and for the same
// reason: a tool that fails on every call is worse than a missing one.
func TestOpen_KnowledgeSearchOnlyWithAnEmbedder(t *testing.T) {
	has := func(t *testing.T, h Host) bool {
		t.Helper()
		w, err := Open(h, "test", t.TempDir())
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(w.Close)
		_, ok := w.snapshot().tools["knowledge_search"]
		return ok
	}

	quiet := slog.New(slog.DiscardHandler)
	if has(t, Host{LLM: llmStub{}, Log: quiet}) {
		t.Error("knowledge_search is registered with no embedding endpoint configured")
	}
	if !has(t, Host{LLM: llmStub{}, Embed: embed.Config{BaseURL: "https://gateway.example"}, Log: quiet}) {
		t.Error("knowledge_search is missing although an endpoint is configured")
	}
}

// The corpus is INSIDE the mount because documents are data; the index is outside it because it is
// host state the model must not be able to rewrite.
func TestOpen_KnowledgeCorpusIsInTheMountAndTheIndexIsNot(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(Host{
		LLM:   llmStub{},
		Embed: embed.Config{BaseURL: "https://gateway.example"},
		Log:   slog.New(slog.DiscardHandler),
	}, "test", dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(w.Close)

	k := w.Knowledge()
	if k == nil {
		t.Fatal("no knowledge store")
	}
	if want := filepath.Join(dir, "mnt", "knowledge"); k.Dir() != want {
		t.Errorf("corpus at %s, want %s — inside the mount", k.Dir(), want)
	}
	if want := filepath.Join(dir, "knowledge.idx.json"); k.IndexPath() != want {
		t.Errorf("index at %s, want %s — outside the mount", k.IndexPath(), want)
	}
	// Stated as a property rather than as two paths: no file tool may reach the index.
	if strings.HasPrefix(k.IndexPath(), filepath.Join(dir, "mnt")+string(filepath.Separator)) {
		t.Error("the index is inside the mount, where a file tool could rewrite it")
	}
}

// The button has to mean what it says. A human answering "always" must produce a grant that survives
// a restart — which means the policy's ceiling has to allow it, the store has to write it, and a
// fresh workspace has to read it back and stop asking.
//
// This is end-to-end on purpose: the ceiling was RecallSession for a long time, and every layer
// below it worked, so nothing failed. The button simply resolved to a session grant and the person
// was asked again the next day.
func TestPolicy_AlwaysSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	act := gate.Action{Kind: tools.NetKind, Target: "api.example.com"}

	// First run: a human answers "always".
	approver := &alwaysApprover{}
	grants, err := newGrantStore(filepath.Join(dir, "grants.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := gate.With(context.Background(), policy(), grants, approver)
	if err := gate.Check(ctx, act, tools.HostMatch); err != nil {
		t.Fatalf("first check: %v", err)
	}
	if approver.asked != 1 {
		t.Fatalf("asked %d times on the first call, want 1", approver.asked)
	}

	// It reached the disk, not just memory.
	raw, err := os.ReadFile(filepath.Join(dir, "grants.json"))
	if err != nil {
		t.Fatalf("grants.json was never written: %v", err)
	}
	if !strings.Contains(string(raw), "api.example.com") {
		t.Errorf("grants.json does not hold the grant: %s", raw)
	}

	// A fresh store over the same file — a restart — must not ask again.
	reopened, err := newGrantStore(filepath.Join(dir, "grants.json"))
	if err != nil {
		t.Fatal(err)
	}
	approver.asked = 0
	ctx2 := gate.With(context.Background(), policy(), reopened, approver)
	if err := gate.Check(ctx2, act, tools.HostMatch); err != nil {
		t.Fatalf("after restart: %v", err)
	}
	if approver.asked != 0 {
		t.Error("the human was asked again after answering always — the grant did not survive")
	}
}

// alwaysApprover is a human who says yes and picks "always", and counts how often it was asked.
type alwaysApprover struct{ asked int }

func (a *alwaysApprover) Ask(_ context.Context, act gate.Action, _ gate.Recall, _ []gate.Grant) (bool, gate.Grant, gate.Recall, error) {
	a.asked++
	return true, gate.Grant{Kind: act.Kind, Target: act.Target}, gate.RecallAlways, nil
}

// A room may not grant anything durable: whoever is audible can speak, so a spoken "always" would be
// a standing permission granted by an unauthenticated channel.
func TestVoicePolicy_NeverRemembers(t *testing.T) {
	if got := voicePolicy(map[string]bool{tools.NetKind: true}).Decide(gate.Action{Kind: tools.NetKind}); got != gate.AskWith(gate.RecallNever) {
		t.Errorf("voice policy on net = %+v, want ask with no recall", got)
	}
}

// An agent's cage must not leak a code_run that dispatches over the full base set.
//
// This was a real escape, and an invisible one. The fired-agent runtime SELECTED the agent's tools
// by name out of the already-composed workspace toolset, which holds the root code_run — whose
// dispatch set is the whole base, captured when it was built. An agent declaring
// [file_read, code_run] listed exactly those two and had a script that could call file_write.
func TestAgentCage_CodeRunDispatchesOverTheCageOnly(t *testing.T) {
	base, err := agentkit.NewToolSet(
		stubTool(t, "file_read"), stubTool(t, "file_write"),
		stubTool(t, "http_write"), stubTool(t, "memory_write"),
	)
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	// What the root chat gets: every base tool, plus a code_run over all of them.
	root, err := tools.Compose(base, true)
	if err != nil {
		t.Fatalf("root: %v", err)
	}

	narrow := agent.Agent{Name: "narrow", Tools: []string{"file_read", "code_run"}}
	cage, err := agentCage(base, narrow)
	if err != nil {
		t.Fatalf("agentCage: %v", err)
	}

	for _, name := range []string{"file_read", "code_run"} {
		if _, ok := cage[name]; !ok {
			t.Errorf("%s was declared and is missing from the cage", name)
		}
	}
	for _, name := range []string{"file_write", "http_write", "memory_write"} {
		if _, ok := cage[name]; ok {
			t.Errorf("%s is in the cage of an agent that did not declare it", name)
		}
	}

	// The reach that matters is the SCRIPT's, not the model's: both sets list a code_run, and the
	// escape was that they were the same one. Asked for a tool it may not have, the caged script must
	// come back empty-handed while the root script gets through.
	const script = `{"source":"console.log(nocturn.call('file_write', {}))"}`
	if out, err := root["code_run"].Call(t.Context(), script); err != nil || !strings.Contains(out, "ok") {
		t.Fatalf("the root script cannot reach file_write, so this proves nothing: %q, %v", out, err)
	}
	out, err := cage["code_run"].Call(t.Context(), script)
	if err == nil && strings.Contains(out, "ok") {
		t.Errorf("a script in a read-only agent reached file_write: %q", out)
	}
}

// stubTool is a base tool that exists to be caged: it has a name and does nothing.
func stubTool(t *testing.T, name string) agentkit.Tool {
	t.Helper()
	tool, err := agentkit.NewTool(name, "stub", func(context.Context, string) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("stub %s: %v", name, err)
	}
	return tool
}
