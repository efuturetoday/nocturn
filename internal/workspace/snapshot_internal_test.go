package workspace

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/plugin"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// openForReload opens a workspace under dir with nothing configured but an LLM stub.
func openForReload(t *testing.T, dir string) *Workspace {
	t.Helper()
	w, err := Open(Host{LLM: llmStub{}, Log: slog.New(slog.DiscardHandler)}, "test", dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(w.Close)
	return w
}

// writeSkill lays down a valid skill (skills/<dir>/SKILL.md) under root.
func writeSkill(t *testing.T, root, dirName, name string) {
	t.Helper()
	sdir := filepath.Join(root, "skills", dirName)
	if err := os.MkdirAll(sdir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: a test skill\n---\n\nDo the thing.\n"
	if err := os.WriteFile(filepath.Join(sdir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestReload_PicksUpASkillAddedAfterOpen is the whole point of the split: discovery runs again, and
// what it finds reaches the toolset without the process restarting.
func TestReload_PicksUpASkillAddedAfterOpen(t *testing.T) {
	dir := t.TempDir()
	w := openForReload(t, dir)

	if got := w.Inventory().Skills; len(got) != 0 {
		t.Fatalf("skills before the skill exists = %v, want none", got)
	}
	if _, ok := w.snapshot().tools["skill_read"]; ok {
		t.Fatal("skill_read exists with no skills — it is the one base tool discovery decides")
	}

	writeSkill(t, dir, "deploy", "deploy")
	if err := w.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if got := w.Inventory().Skills; !slices.Equal(got, []string{"deploy"}) {
		t.Fatalf("skills after Reload = %v, want [deploy]", got)
	}
	if _, ok := w.snapshot().tools["skill_read"]; !ok {
		t.Fatal("skill_read is missing although a skill was discovered")
	}
}

// recordingLLM answers every turn immediately and records the tool names it was offered, so a test
// can see exactly what the model was told it had on each turn.
type recordingLLM struct {
	mu     sync.Mutex
	turns  [][]string
	called chan struct{}
}

func newRecordingLLM() *recordingLLM { return &recordingLLM{called: make(chan struct{}, 8)} }

func (l *recordingLLM) Next(_ context.Context, _ []agentkit.Message, specs []agentkit.ToolSpec) (agentkit.Step, error) {
	names := make([]string, 0, len(specs))
	for _, sp := range specs {
		names = append(names, sp.Name)
	}
	slices.Sort(names)
	l.mu.Lock()
	l.turns = append(l.turns, names)
	l.mu.Unlock()
	l.called <- struct{}{}
	return agentkit.Step{Answer: "ok"}, nil
}

func (l *recordingLLM) await(t *testing.T) []string {
	t.Helper()
	select {
	case <-l.called:
	case <-time.After(5 * time.Second):
		t.Fatal("the model was never asked")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.turns[len(l.turns)-1]
}

// TestReload_OpenSessionSeesNewToolsOnItsNextTurn is the promise the whole split exists for: install
// something while a conversation is open, and the very NEXT message has it — no reopening, no waiting
// for the session to be reaped, and nothing interrupted.
//
// The turn is the boundary, not the session. A turn is handed one tool list at its start and works
// with it throughout, so a tool can never vanish between two calls the model already planned
// together; the turn after that simply sees the new world.
func TestReload_OpenSessionSeesNewToolsOnItsNextTurn(t *testing.T) {
	dir := t.TempDir()
	llm := newRecordingLLM()
	w, err := Open(Host{LLM: llm, Log: slog.New(slog.DiscardHandler)}, "test", dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(w.Close)

	w.Chats().Submit("c1", "first")
	first := llm.await(t)
	if slices.Contains(first, "skill_read") {
		t.Fatalf("skill_read offered before any skill exists: %v", first)
	}

	// Install a skill into the SAME open conversation.
	writeSkill(t, dir, "deploy", "deploy")
	if err := w.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	w.Chats().Submit("c1", "second")
	second := llm.await(t)
	if !slices.Contains(second, "skill_read") {
		t.Fatalf("the next turn of an OPEN chat did not see the new skill's tool: %v", second)
	}
	if !slices.Contains(second, "skill_load") {
		t.Fatalf("the next turn did not see the skill catalog's load tool: %v", second)
	}
}

// TestReload_FailureLeavesThePreviousAssemblyStanding: a workspace is never half-swapped. A plugin
// whose namespaced tool name collides with an existing tool is a hard error (unlike a bad skill,
// which is skipped), so it is what a failing assembly looks like in practice.
func TestReload_FailureLeavesThePreviousAssemblyStanding(t *testing.T) {
	dir := t.TempDir()
	w := openForReload(t, dir)

	writePlugin(t, dir, "weather", "now", nil)
	if err := w.Reload(); err != nil {
		t.Fatalf("Reload with one plugin: %v", err)
	}
	good := w.snapshot()
	if got := w.Inventory().Plugins; !slices.Equal(got, []string{"weather"}) {
		t.Fatalf("plugins = %v, want [weather]", got)
	}

	// Folder "file" + tool "read" namespaces to file_read, which the base toolset already owns. The
	// folder name is the plugin's identity (discovery.ResolveName), so this is the collision.
	writePlugin(t, dir, "file", "read", nil)

	if err := w.Reload(); err == nil {
		t.Fatal("a colliding plugin tool must fail the assembly, not be silently dropped")
	}
	if w.snapshot() != good {
		t.Fatal("a failed Reload replaced the assembly — the workspace must be left exactly as it was")
	}
	if got := w.Inventory().Plugins; !slices.Equal(got, []string{"weather"}) {
		t.Fatalf("plugins after the failed Reload = %v, want the previous [weather]", got)
	}
}

// TestReload_DoesNotAccumulateInjectorBindings: assemble runs again on every reload, and AddBinding
// appends. Without clearing each owner's bindings first, a plugin's credential would be injected once
// per reload — and a plugin deleted from disk would keep injecting for the life of the process.
func TestReload_DoesNotAccumulateInjectorBindings(t *testing.T) {
	dir := t.TempDir()
	m := testMaster(t)
	w, err := Open(Host{LLM: llmStub{}, Master: m, Log: slog.New(slog.DiscardHandler)}, "test", dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(w.Close)

	writePlugin(t, dir, "weather", "now", []secret.Binding{{Secret: "api_key", Host: "api.example.com", Header: "Authorization"}})
	w.sec.resolution.Set(plugin.SecretName("weather", "api_key"), []byte("v"))

	// InjectMatching names every credential it stamped, so a duplicated binding shows up as the same
	// name twice — the exact observable, through the public API.
	inject := func() []string {
		t.Helper()
		ctx := secret.WithOwner(t.Context(), plugin.Owner("weather"))
		names, err := w.sec.injector.InjectMatching(ctx, &secret.Request{Headers: map[string]string{}}, "api.example.com")
		if err != nil {
			t.Fatalf("InjectMatching: %v", err)
		}
		return names
	}

	for range 3 {
		if err := w.Reload(); err != nil {
			t.Fatalf("Reload: %v", err)
		}
	}
	if got := inject(); len(got) != 1 {
		t.Fatalf("credentials injected after three reloads = %v, want exactly one", got)
	}

	// Removing the plugin from disk must take its binding with it. This is the half that matters: a
	// deleted plugin whose credential still rides along on requests to its host is authority that
	// outlived the thing it was granted to.
	if err := os.RemoveAll(filepath.Join(dir, "plugins", "weather")); err != nil {
		t.Fatal(err)
	}
	if err := w.Reload(); err != nil {
		t.Fatalf("Reload after removal: %v", err)
	}
	if got := inject(); len(got) != 0 {
		t.Fatalf("a removed plugin still injects %v", got)
	}
}

// TestInventory_IsConsistentUnderReload: Inventory must report ONE assembly, never a mix. Run under
// -race, this is also the detector for the torn read the old two-lock arrangement allowed.
func TestInventory_IsConsistentUnderReload(t *testing.T) {
	dir := t.TempDir()
	w := openForReload(t, dir)
	writeSkill(t, dir, "deploy", "deploy")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 20 {
			if err := w.Reload(); err != nil {
				t.Errorf("Reload: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 200 {
			inv := w.Inventory()
			// The one cross-field invariant discovery guarantees: skill_read is in the toolset if and
			// only if a skill was discovered. A read spanning two assemblies would break it.
			hasRead := slices.Contains(inv.Tools, "skill_read")
			if hasRead != (len(inv.Skills) > 0) {
				t.Errorf("torn inventory: skills=%v but skill_read present=%v", inv.Skills, hasRead)
				return
			}
		}
	}()
	wg.Wait()
}
