package approval_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/internal/approval"
)

func TestStatus_NewThenApprovedThenChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approved.json")
	s := approval.Load(path)

	v1 := []byte(`{"url":"https://a.example.com"}`)

	// New: not approved, no prior.
	if ok, prior := s.Status("mcp", "x", v1); ok || prior != nil {
		t.Fatalf("new entry: ok=%v prior=%q, want false,nil", ok, prior)
	}
	if err := s.Approve("mcp", "x", v1); err != nil {
		t.Fatal(err)
	}
	// Same content: approved.
	if ok, _ := s.Status("mcp", "x", v1); !ok {
		t.Fatal("identical content should be approved")
	}
	// Changed content: not approved, prior is the old declaration (for the diff).
	v2 := []byte(`{"url":"https://evil.example.com"}`)
	ok, prior := s.Status("mcp", "x", v2)
	if ok {
		t.Fatal("changed content must not be approved")
	}
	if string(prior) != string(v1) {
		t.Fatalf("prior = %q, want the previously approved %q", prior, v1)
	}
}

// The record survives a reload, and equality holds despite the file being
// pretty-printed (hash-based, not byte-of-serialized-form).
func TestApprove_PersistsAcrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approved.json")
	content := []byte(`{"manifest":{"name":"gmail"},"artifact_sha256":"abc"}`)
	if err := approval.Load(path).Approve("plugin", "gmail", content); err != nil {
		t.Fatal(err)
	}
	if ok, _ := approval.Load(path).Status("plugin", "gmail", content); !ok {
		t.Fatal("approval did not survive reload")
	}
	// Kind+name namespacing: same name, other kind is independent.
	if ok, _ := approval.Load(path).Status("mcp", "gmail", content); ok {
		t.Fatal("kinds must not collide")
	}
}

// A missing or corrupt file yields an empty store — fail-safe, never
// auto-approving on unreadable state.
func TestLoad_MissingOrCorrupt_FailsSafe(t *testing.T) {
	content := []byte(`{"x":1}`)

	if ok, _ := approval.Load(filepath.Join(t.TempDir(), "absent.json")).Status("plugin", "p", content); ok {
		t.Fatal("missing file must not report approved")
	}

	corrupt := filepath.Join(t.TempDir(), "approved.json")
	if err := os.WriteFile(corrupt, []byte("not json{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, _ := approval.Load(corrupt).Status("plugin", "p", content); ok {
		t.Fatal("corrupt file must not report approved")
	}
}

// The persisted file is 0600 and the temp file is not left behind.
func TestApprove_FilePerms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approved.json")
	if err := approval.Load(path).Approve("plugin", "p", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v, want 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temp file left behind")
	}
}
