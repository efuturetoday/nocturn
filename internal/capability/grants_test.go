package capability_test

import (
	"fmt"
	"testing"

	"github.com/efuturetoday/nocturn/internal/capability"
)

// memStore is an in-memory GrantStore for asserting tool-scoped "always" grants.
type memStore struct{ recs map[string]bool }

func (m *memStore) key(tool string, c capability.Call) string {
	return fmt.Sprintf("%s|%s|%v|%s", tool, c.Family, c.Mutates, c.Target)
}
func (m *memStore) Allows(tool string, c capability.Call) bool {
	return m.recs[m.key(tool, c)]
}
func (m *memStore) Record(tool string, c capability.Call) error {
	m.recs[m.key(tool, c)] = true
	return nil
}

// The core §7 fix: an "always" grant is remembered against the model-facing TOOL,
// so approving "gmail.send" never silently covers "gmail.delete" on the SAME host.
// Before tool-scoping, both collapsed to (http.write, gmail.googleapis.com) and one
// "allow always" opened the whole host to every tool — the prompt-injection hole.
func TestGrants_AlwaysIsToolScoped(t *testing.T) {
	store := &memStore{recs: map[string]bool{}}
	g := capability.NewGrants(capability.Permanent, store)

	call := capability.Call{Family: "http", Mutates: true, Target: "gmail.googleapis.com"}
	if err := g.Record("gmail.send", call, capability.ScopeAlways); err != nil {
		t.Fatalf("record: %v", err)
	}

	if !g.Allows("gmail.send", call, capability.Env{}) {
		t.Fatal("the granted tool must be allowed")
	}
	if g.Allows("gmail.delete", call, capability.Env{}) {
		t.Fatal("a DIFFERENT tool on the same (capability, host) must NOT be covered — the hole")
	}
	if g.Allows("", call, capability.Env{}) {
		t.Fatal("a direct (no outermost tool) call must not ride a tool-scoped grant")
	}
}

// Session grants are equally tool-scoped, and bound to the grants' epoch: closing
// the epoch revokes them even for the granted tool.
func TestGrants_SessionIsToolScopedAndEpochBound(t *testing.T) {
	reg := capability.NewEpochRegistry()
	ep := reg.Open()
	g := capability.NewGrants(ep, nil)
	env := capability.Env{Epochs: reg}

	call := capability.Call{Family: "http", Mutates: true, Target: "api.github.com"}
	if err := g.Record("github.create_issue", call, capability.ScopeSession); err != nil {
		t.Fatalf("record: %v", err)
	}

	if !g.Allows("github.create_issue", call, env) {
		t.Fatal("the granted tool must be allowed this session")
	}
	if g.Allows("github.delete_repo", call, env) {
		t.Fatal("a different tool must not ride the session grant")
	}

	reg.Close(ep)
	if g.Allows("github.create_issue", call, env) {
		t.Fatal("closing the epoch must revoke the session grant")
	}
}
