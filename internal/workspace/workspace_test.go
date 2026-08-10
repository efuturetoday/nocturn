package workspace_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/tools"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// fakeLLM answers every turn immediately with a fixed final answer — no tool calls, no streamed
// tokens. It drives a chat turn end-to-end (through the Manager) without a real endpoint.
type fakeLLM struct{}

func (fakeLLM) Next(_ context.Context, _ []agentkit.Message, _ []agentkit.ToolSpec) (agentkit.Step, error) {
	return agentkit.Step{Answer: "ok"}, nil
}

// answerLLM streams its answer as a top-level (Frame 0) token then returns it as the final step, so a
// run's persisted transcript ends with that assistant message.
type answerLLM struct{ text string }

func (a answerLLM) Next(ctx context.Context, _ []agentkit.Message, _ []agentkit.ToolSpec) (agentkit.Step, error) {
	agentkit.Emit(ctx, agentkit.Token{Text: a.text})
	return agentkit.Step{Answer: a.text}, nil
}

// toolCallerLLM issues one tool call named by the "call:<tool>" user directive, then — once it sees
// the resulting tool message — echoes that result as the streamed answer. So the tool's outcome
// (a denial, an "unknown tool", or a real result) surfaces as the run's final assistant message.
type toolCallerLLM struct{}

func (toolCallerLLM) Next(ctx context.Context, conv []agentkit.Message, _ []agentkit.ToolSpec) (agentkit.Step, error) {
	if n := len(conv); n > 0 && conv[n-1].Role == agentkit.RoleTool {
		out := conv[n-1].Content
		agentkit.Emit(ctx, agentkit.Token{Text: out})
		return agentkit.Step{Answer: out}, nil
	}
	tool := ""
	for i := len(conv) - 1; i >= 0; i-- {
		if conv[i].Role == agentkit.RoleUser {
			tool = strings.TrimPrefix(conv[i].Content, "call:")
			break
		}
	}
	return agentkit.Step{ToolCalls: []agentkit.ToolCall{{ID: "c1", Tool: tool, Args: `{"host":"example.com"}`}}}, nil
}

// openWSDir opens a workspace rooted at dir with the given LLM and closes its chat manager on cleanup.
func openWSDir(t *testing.T, llm agentkit.LLM, dir string) *workspace.Workspace {
	t.Helper()
	h := workspace.Host{LLM: llm, Log: slog.New(slog.DiscardHandler)}
	w, err := workspace.Open(h, "test", dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(w.Close)
	return w
}

// openWS opens a workspace in a fresh temp dir.
func openWS(t *testing.T, llm agentkit.LLM) *workspace.Workspace {
	t.Helper()
	return openWSDir(t, llm, t.TempDir())
}

// writeAgent declares an agent (agents/<name>/agent.md) under dir BEFORE Open, so Discover picks it
// up. tools become the agent's cage filter (empty = a pure reasoner).
func writeAgent(t *testing.T, dir, name string, tools []string) {
	t.Helper()
	adir := filepath.Join(dir, "agents", name)
	if err := os.MkdirAll(adir, 0o700); err != nil {
		t.Fatalf("mkdir agent: %v", err)
	}
	var b strings.Builder
	b.WriteString("---\nname: " + name + "\n")
	if len(tools) > 0 {
		b.WriteString("tools:\n")
		for _, tl := range tools {
			b.WriteString("  - " + tl + "\n")
		}
	}
	b.WriteString("---\nYou are " + name + ".\n")
	if err := os.WriteFile(filepath.Join(adir, "agent.md"), []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write agent.md: %v", err)
	}
}

// eventually polls cond for up to a second — for the async Manager turn to land.
func eventually(cond func() bool) bool {
	for range 200 {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// TestWorkspace_Open_BuildsIsolatedStack verifies Open lays down the isolated stack: the LLM mount
// and the two separate chat stores (user chats vs agent runs) as sibling dirs of the control plane.
//
// NOTE (discrepancy vs plan): grants.json is NOT created at Open — newGrantStore only READS it
// (missing = empty seed); the file appears lazily on the first durable grant. So this asserts the
// dirs that Open does create, not a grants.json that it does not.
func TestWorkspace_Open_BuildsIsolatedStack(t *testing.T) {
	dir := t.TempDir()
	openWSDir(t, fakeLLM{}, dir)

	for _, sub := range []string{"mnt", "chats", "agent-runs"} {
		info, err := os.Stat(filepath.Join(dir, sub))
		if err != nil || !info.IsDir() {
			t.Errorf("Open must create %q as a dir (err %v)", sub, err)
		}
	}
	// The LLM mount is a sibling of the control plane, never rooted at dir — grants.json must be
	// unreachable from the mount.
	if _, err := os.Stat(filepath.Join(dir, "mnt", "grants.json")); !os.IsNotExist(err) {
		t.Errorf("grants.json must not live inside the mount (err %v)", err)
	}
}

// TestWorkspace_Open_UserAndAgentStoresSeparate proves the user chat store and the agent-run store
// are distinct: a user chat lands only in Chats (source user), a fired agent run only in AgentRuns
// (source agent).
func TestWorkspace_Open_UserAndAgentStoresSeparate(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "helper", nil)
	w := openWSDir(t, answerLLM{text: "done"}, dir)

	uid := chat.NewID()
	w.Chats().Submit(uid, "hello there")
	if !eventually(func() bool {
		msgs, _ := w.Chats().Transcript(uid)
		return len(msgs) >= 2
	}) {
		t.Fatal("user chat turn did not persist")
	}
	if _, err := w.FireAgent(t.Context(), "helper", "do it"); err != nil {
		t.Fatalf("FireAgent: %v", err)
	}
	// FireAgent is async: wait for the run to persist to the agent store.
	if !eventually(func() bool {
		runs, _ := w.AgentRuns()
		return len(runs) == 1
	}) {
		t.Fatal("agent run did not persist")
	}

	users, err := w.Chats().List()
	if err != nil {
		t.Fatalf("Chats.List: %v", err)
	}
	if len(users) != 1 || users[0].ID != uid || users[0].Source != chat.SourceUser {
		t.Errorf("user store = %+v, want exactly chat %q source user", users, uid)
	}
	runs, err := w.AgentRuns()
	if err != nil {
		t.Fatalf("AgentRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].Source != chat.SourceAgent {
		t.Errorf("agent store = %+v, want exactly one agent-source run", runs)
	}
	// Cross-contamination check: the user chat id is absent from the agent store and vice versa.
	if runs[0].ID == uid {
		t.Error("agent run reused the user chat id — stores are not isolated")
	}
}

// TestWorkspace_Open_TwoWorkspaces_Isolated opens two workspaces with different declared agents and
// verifies each sees only its own — the stacks are assembled per-dir, not shared.
func TestWorkspace_Open_TwoWorkspaces_Isolated(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	writeAgent(t, dirA, "alpha", nil)
	writeAgent(t, dirB, "beta", nil)

	wa := openWSDir(t, fakeLLM{}, dirA)
	wb := openWSDir(t, fakeLLM{}, dirB)

	names := func(w *workspace.Workspace) []string {
		var out []string
		for _, a := range w.Agents() {
			out = append(out, a.Name)
		}
		return out
	}
	if got := names(wa); len(got) != 1 || got[0] != "alpha" {
		t.Errorf("workspace A agents = %v, want [alpha]", got)
	}
	if got := names(wb); len(got) != 1 || got[0] != "beta" {
		t.Errorf("workspace B agents = %v, want [beta]", got)
	}
}

// TestOpenAll_AlwaysIncludesMain: OpenAll over an empty root still yields the default "main".
func TestOpenAll_AlwaysIncludesMain(t *testing.T) {
	h := workspace.Host{LLM: fakeLLM{}, Log: slog.New(slog.DiscardHandler)}
	spaces, err := workspace.OpenAll(h, t.TempDir())
	if err != nil {
		t.Fatalf("OpenAll: %v", err)
	}
	t.Cleanup(func() {
		for _, w := range spaces {
			w.Close()
		}
	})
	main, ok := spaces[workspace.DefaultWorkspace]
	if !ok {
		t.Fatalf("OpenAll must always include %q; got %v", workspace.DefaultWorkspace, keys(spaces))
	}
	if main.Name() != workspace.DefaultWorkspace {
		t.Errorf("main workspace name = %q, want %q", main.Name(), workspace.DefaultWorkspace)
	}
}

// TestOpenAll_OpensEachSubdir: each subdirectory of root becomes a workspace by name, plus main.
func TestOpenAll_OpensEachSubdir(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"work", "home"} {
		if err := os.MkdirAll(filepath.Join(root, n), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	h := workspace.Host{LLM: fakeLLM{}, Log: slog.New(slog.DiscardHandler)}
	spaces, err := workspace.OpenAll(h, root)
	if err != nil {
		t.Fatalf("OpenAll: %v", err)
	}
	t.Cleanup(func() {
		for _, w := range spaces {
			w.Close()
		}
	})
	for _, want := range []string{"work", "home", workspace.DefaultWorkspace} {
		if _, ok := spaces[want]; !ok {
			t.Errorf("OpenAll missing workspace %q; got %v", want, keys(spaces))
		}
	}
}

func keys(m map[string]*workspace.Workspace) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// setupMarkRead builds a workspace with one user chat and one agent run, both persisted and unread,
// returning their ids.
func setupMarkRead(t *testing.T) (w *workspace.Workspace, userID, agentID string) {
	t.Helper()
	dir := t.TempDir()
	writeAgent(t, dir, "helper", nil)
	w = openWSDir(t, answerLLM{text: "done"}, dir)

	userID = chat.NewID()
	w.Chats().Submit(userID, "a user question")
	if !eventually(func() bool {
		msgs, _ := w.Chats().Transcript(userID)
		return len(msgs) >= 2
	}) {
		t.Fatal("user chat did not persist")
	}
	if _, err := w.FireAgent(t.Context(), "helper", "run"); err != nil {
		t.Fatalf("FireAgent: %v", err)
	}
	if !eventually(func() bool {
		runs, _ := w.AgentRuns()
		return len(runs) == 1
	}) {
		t.Fatal("agent run did not persist")
	}
	runs, err := w.AgentRuns()
	if err != nil || len(runs) != 1 {
		t.Fatalf("AgentRuns = %v (err %v), want 1", runs, err)
	}
	return w, userID, runs[0].ID
}

func isRead(t *testing.T, metas []chat.Meta, id string) bool {
	t.Helper()
	for _, m := range metas {
		if m.ID == id {
			return !m.Read.IsZero() && m.Read.Equal(m.Updated)
		}
	}
	t.Fatalf("chat %q not found in %+v", id, metas)
	return false
}

// TestWorkspace_MarkRead_KindSelectsStore: MarkRead advances the cursor in the kind-selected store
// only — marking the user store leaves the agent run untouched, and vice versa.
func TestWorkspace_MarkRead_KindSelectsStore(t *testing.T) {
	w, userID, agentID := setupMarkRead(t)

	w.MarkRead("user", userID)
	users, _ := w.Chats().List()
	runs, _ := w.AgentRuns()
	if !isRead(t, users, userID) {
		t.Error(`MarkRead("user", userID) did not mark the user chat read`)
	}
	if isRead(t, runs, agentID) {
		t.Error(`MarkRead("user", …) must not touch the agent store`)
	}

	w.MarkRead("agent", agentID)
	runs, _ = w.AgentRuns()
	if !isRead(t, runs, agentID) {
		t.Error(`MarkRead("agent", agentID) did not mark the agent run read`)
	}
}

// TestWorkspace_OnChatUpdate_WiresBothStores: the OnChatUpdate callback fires for saves in BOTH the
// user store and the agent store — proving both are wired to it.
func TestWorkspace_OnChatUpdate_WiresBothStores(t *testing.T) {
	w, userID, agentID := setupMarkRead(t)

	var mu sync.Mutex
	seen := map[chat.Source]bool{}
	w.OnChatUpdate(func(m chat.Meta) {
		mu.Lock()
		seen[m.Source] = true
		mu.Unlock()
	})

	// MarkRead is a synchronous persist that fires the save callback; drive one on each store.
	w.MarkRead("user", userID)
	w.MarkRead("agent", agentID)

	mu.Lock()
	defer mu.Unlock()
	if !seen[chat.SourceUser] {
		t.Error("OnChatUpdate did not fire for a user-store save")
	}
	if !seen[chat.SourceAgent] {
		t.Error("OnChatUpdate did not fire for an agent-store save")
	}
}

// TestWorkspace_Open_RestoresPersistedWakes proves the wake store is wired to <ws>/wakes.json and
// restored only once the lookup seam is bound.
//
// A wake that came due while the process was down must resume its chat, which is the whole reason the
// pending set is now persisted at all: it used to live only in a time.AfterFunc, so a restart dropped
// every outstanding continuation with no log line and no error. The ordering is load-bearing too — an
// overdue wake fires the instant its timer is armed, and firing consumes it, so arming before Bind
// would lose exactly what the store exists to keep.
func TestWorkspace_Open_RestoresPersistedWakes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		overdue := []tools.Wake{{
			ID:     "wake-1",
			FireAt: time.Now().Add(-time.Hour),
			ChatID: "c1",
			Note:   "re-check the deploy",
		}}
		data, err := json.Marshal(overdue)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "wakes.json"), data, 0o600); err != nil {
			t.Fatalf("write wakes.json: %v", err)
		}

		h := workspace.Host{LLM: fakeLLM{}, Log: slog.New(slog.DiscardHandler)}
		w, err := workspace.Open(h, "test", dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		synctest.Wait()

		msgs, err := w.Chats().Transcript("c1")
		if err != nil {
			t.Fatalf("Transcript: %v", err)
		}
		if len(msgs) == 0 {
			t.Fatal("the restored wake never resumed its chat — no transcript for c1")
		}
		if msgs[0].Content != "re-check the deploy" {
			t.Fatalf("resumed chat opened with %q, want the wake note", msgs[0].Content)
		}

		w.Close()
		synctest.Wait()
	})
}
