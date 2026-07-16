package agent_test

import (
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/capability"
)

// Per-owner isolation is STRUCTURAL: two owners are two files, so a grant recorded
// by one can never be seen by the other — even for the identical (tool, call).
func TestGrantsStore_PerOwnerIsolation(t *testing.T) {
	dir := t.TempDir()
	sessionStore := agent.LoadGrantsStore(filepath.Join(dir, "grants.json"))
	agentStore := agent.LoadGrantsStore(agent.GrantsPath(filepath.Join(dir, "agents"), "triage"))

	call := capability.Call{Family: "http", Write: true, Target: "api.github.com"}
	if err := sessionStore.Record("github.create_issue", call); err != nil {
		t.Fatalf("record: %v", err)
	}

	if !sessionStore.Allows("github.create_issue", call) {
		t.Fatal("the recording owner must see its own grant")
	}
	if agentStore.Allows("github.create_issue", call) {
		t.Fatal("a DIFFERENT owner (agent) must NOT see the session's grant — structural isolation")
	}
}

// A grant survives a reload of the SAME file (persistence), and Remove revokes it.
func TestGrantsStore_PersistAndRevoke(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	call := capability.Call{Family: "http", Write: true, Target: "gmail.googleapis.com"}

	s := agent.LoadGrantsStore(path)
	if err := s.Record("gmail.send", call); err != nil {
		t.Fatalf("record: %v", err)
	}

	reloaded := agent.LoadGrantsStore(path)
	if !reloaded.Allows("gmail.send", call) {
		t.Fatal("an always-grant must survive a reload of the same file")
	}
	list := reloaded.List()
	if len(list) != 1 || list[0].Tool != "gmail.send" {
		t.Fatalf("List() = %+v, want one gmail.send grant", list)
	}
	if err := reloaded.Remove(list[0]); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if reloaded.Allows("gmail.send", call) {
		t.Fatal("a revoked grant must no longer apply")
	}
	if fresh := agent.LoadGrantsStore(path); fresh.Allows("gmail.send", call) {
		t.Fatal("the revoke must persist to the file")
	}
}
