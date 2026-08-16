package workspace_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// recordingApprover implements gate.Approver: it records that it was asked and returns a fixed
// verdict. It stands in for the out-of-band human (the phone) so a test can prove a guarded firing
// reaches an approver — where a strict firing has none.
type recordingApprover struct {
	mu       sync.Mutex
	asked    bool
	approve  bool
	remember gate.Grant
}

var _ gate.Approver = (*recordingApprover)(nil)

func (r *recordingApprover) Ask(_ context.Context, _ gate.Action, _ gate.Recall, _ []gate.Grant) (bool, gate.Grant, gate.Recall, error) {
	r.mu.Lock()
	r.asked = true
	r.mu.Unlock()
	return r.approve, r.remember, gate.RecallNever, nil
}

func (r *recordingApprover) wasAsked() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.asked
}

// openWSApprover opens a workspace rooted at dir with the given LLM and out-of-band approver.
func openWSApprover(t *testing.T, llm agentkit.LLM, dir string, appr gate.Approver) *workspace.Workspace {
	t.Helper()
	h := workspace.Host{LLM: llm, Approver: appr, Log: slog.New(slog.DiscardHandler)}
	w, err := workspace.Open(h, "test", dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(w.Close)
	return w
}

// writeGuardedAgent declares a guarded agent (autonomy: guarded) with the given cage under dir.
func writeGuardedAgent(t *testing.T, dir, name string, tools []string) {
	t.Helper()
	adir := filepath.Join(dir, "agents", name)
	if err := os.MkdirAll(adir, 0o700); err != nil {
		t.Fatalf("mkdir agent: %v", err)
	}
	var b strings.Builder
	b.WriteString("---\nname: " + name + "\nautonomy: guarded\n")
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

// fireAndAnswer fires an agent and waits for its run to persist, returning the run's final assistant
// message. FireAgent is fire-and-forget (the run streams through the agent manager like any chat), so
// the test polls the persisted transcript rather than a returned answer.
func fireAndAnswer(t *testing.T, w *workspace.Workspace, name, task string) string {
	t.Helper()
	id, err := w.FireAgent(t.Context(), name, task)
	if err != nil {
		t.Fatalf("FireAgent: %v", err)
	}
	var answer string
	if !eventually(func() bool {
		msgs, _ := w.AgentChats().Transcript(id)
		if len(msgs) == 0 {
			return false
		}
		last := msgs[len(msgs)-1]
		if last.Role != agentkit.RoleAssistant || last.Content == "" {
			return false
		}
		answer = last.Content
		return true
	}) {
		t.Fatal("agent run did not persist a final answer")
	}
	return answer
}

// TestFireAgent_UnknownAgent_Error: firing a name that isn't declared is an error, not a silent run.
func TestFireAgent_UnknownAgent_Error(t *testing.T) {
	w := openWS(t, fakeLLM{})
	if _, err := w.FireAgent(t.Context(), "nope", "task"); err == nil {
		t.Fatal("FireAgent on an unknown agent must error")
	}
}

// TestFireAgent_PersistsToAgentStore: a fired run streams through the agent manager and persists its
// transcript to the AGENT store (source agent), not the user one, with Meta.Agent set to the owner.
func TestFireAgent_PersistsToAgentStore(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "helper", nil)
	w := openWSDir(t, answerLLM{text: "the answer"}, dir)

	if got := fireAndAnswer(t, w, "helper", "do the thing"); got != "the answer" {
		t.Errorf("answer = %q, want %q", got, "the answer")
	}

	runs, err := w.AgentRuns()
	if err != nil {
		t.Fatalf("AgentRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("agent runs = %d, want 1 persisted transcript", len(runs))
	}
	if runs[0].Agent != "helper" {
		t.Errorf("run Meta.Agent = %q, want %q (the owning agent)", runs[0].Agent, "helper")
	}
	// The run landed in the agent store, and nothing leaked into the user store.
	if users, _ := w.Chats().List(); len(users) != 0 {
		t.Errorf("user store = %d chats, want 0 (an agent run must not persist there)", len(users))
	}
}

// TestFireAgent_Strict_NilApprover_FailsClosedOnAsk: a strict firing (the default) runs with no
// approver even when the workspace has one wired, so a gated action that would ask the human is
// DENIED and the approver is never consulted — the tool result carries the denial.
func TestFireAgent_Strict_NilApprover_FailsClosedOnAsk(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "neta", []string{"dns_resolve"}) // no autonomy → strict; dns_resolve asks on the net axis
	appr := &recordingApprover{approve: true}           // would approve IF asked
	w := openWSApprover(t, toolCallerLLM{}, dir, appr)

	answer := fireAndAnswer(t, w, "neta", "call:dns_resolve")
	if appr.wasAsked() {
		t.Error("a strict firing must NOT consult the approver — it is unattended by design")
	}
	if !strings.Contains(answer, "denied") {
		t.Errorf("answer = %q, want the unattended denial (strict must fail closed)", answer)
	}
}

// TestFireAgent_Guarded_RoutesAskToApprover: a guarded firing hands the gate the workspace's
// out-of-band approver, so a net-axis Ask reaches the human instead of being denied unattended. Here
// the approver declines, so the action is still refused — but the point is that it was ASKED, which a
// strict firing (nil approver) never does.
func TestFireAgent_Guarded_RoutesAskToApprover(t *testing.T) {
	dir := t.TempDir()
	writeGuardedAgent(t, dir, "neta", []string{"dns_resolve"})
	appr := &recordingApprover{approve: false}
	w := openWSApprover(t, toolCallerLLM{}, dir, appr)

	answer := fireAndAnswer(t, w, "neta", "call:dns_resolve")
	if !appr.wasAsked() {
		t.Error("a guarded firing must route the Ask to the approver (out-of-band HITL)")
	}
	if !strings.Contains(answer, "denied") {
		t.Errorf("answer = %q, want the declined-by-human denial", answer)
	}
}

// TestFireAgent_CageIsAgentFilteredToolset: the firing's toolset is the workspace set filtered to the
// agent's declared tools — a call outside the cage is unreachable ("unknown tool"), an in-cage call
// runs.
func TestFireAgent_CageIsAgentFilteredToolset(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "caged", []string{"time_now"}) // cage = {time_now}; time_now is ungated
	w := openWSDir(t, toolCallerLLM{}, dir)

	// Out of cage: dns_resolve is filtered out, so the toolset reports it unknown.
	if out := fireAndAnswer(t, w, "caged", "call:dns_resolve"); !strings.Contains(out, "unknown") {
		t.Errorf("out-of-cage answer = %q, want an 'unknown tool' result (cage must exclude it)", out)
	}
	// In cage: time_now is reachable and returns a real result, not an unknown-tool error.
	if in := fireAndAnswer(t, w, "caged", "call:time_now"); strings.Contains(in, "unknown") {
		t.Errorf("in-cage answer = %q, want the tool to run (it is inside the cage)", in)
	}
}

// TestFireAgent_OrphanedRun_ReadOnlyRuntime: a persisted run whose agent was deleted (and the daemon
// restarted) reopens under the read-only runtime — its transcript loads, but it has no tools, so a
// resubmitted tool call is unknown (it cannot act). Proves the resolver's fallback.
func TestFireAgent_OrphanedRun_ReadOnlyRuntime(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "gone", []string{"time_now"})
	w := openWSDir(t, toolCallerLLM{}, dir)

	// Fire once so a run persists with Meta.Agent = "gone".
	id, err := w.FireAgent(t.Context(), "gone", "call:time_now")
	if err != nil {
		t.Fatalf("FireAgent: %v", err)
	}
	if !eventually(func() bool {
		msgs, _ := w.AgentChats().Transcript(id)
		return len(msgs) > 0
	}) {
		t.Fatal("run did not persist")
	}

	// Delete the declaration and reopen the workspace (as a restart would): the run is still in the
	// agent store, but "gone" is no longer a known agent, so the resolver falls back to read-only.
	if err := os.RemoveAll(filepath.Join(dir, "agents", "gone")); err != nil {
		t.Fatal(err)
	}
	w2 := openWSDir(t, toolCallerLLM{}, dir)
	w2.AgentChats().Submit(id, "call:time_now")
	if !eventually(func() bool {
		msgs, _ := w2.AgentChats().Transcript(id)
		last := msgs[len(msgs)-1]
		return last.Role == agentkit.RoleAssistant && strings.Contains(last.Content, "unknown")
	}) {
		t.Error("an orphaned run must reopen under the read-only runtime (no tools)")
	}
}

// TestWorkspace_Close_StopsBackgroundWork proves Close ends what StartAgents owns.
//
// It matters because a workspace can now be deleted while the process lives on. Before this, Close
// stopped the chat managers and left the cron scheduler and the document reconcile running against a
// directory on its way to the trash — invisible while the only caller was daemon shutdown, and a
// leaked goroutine per deleted workspace afterwards.
func TestWorkspace_Close_StopsBackgroundWork(t *testing.T) {
	w := openWS(t, fakeLLM{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.StartAgents(context.Background())
	}()

	// No synchronisation before Close on purpose: whichever wins, StartAgents must end. Close cancels
	// a registered scheduler, and the latch it sets makes a StartAgents that has not registered yet
	// return without starting. The daemon starts these in goroutines, so this interleaving is ordinary.
	w.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StartAgents still running 5s after Close")
	}
}

// TestWorkspace_Close_Idempotent proves Close tolerates being called twice — the registry calls it on
// delete and the daemon calls it again on shutdown, and neither knows about the other.
func TestWorkspace_Close_Idempotent(t *testing.T) {
	w := openWS(t, fakeLLM{})
	w.Close()
	w.Close() // must not panic
}
