package tools_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/app/secret"
	"github.com/efuturetoday/nocturn/app/tools"
)

// fileToolWithScanner builds the base toolset over root with a leak scanner installed and returns the
// named file tool — used to exercise ingress redaction on reads.
func fileToolWithScanner(t *testing.T, root, name string, sc *secret.Scanner) agentkit.Tool {
	t.Helper()
	ts, err := tools.Base(tools.Config{Root: root, Scanner: sc})
	if err != nil {
		t.Fatalf("Base: %v", err)
	}
	for _, tl := range ts {
		if tl.Spec().Name == name {
			return tl
		}
	}
	t.Fatalf("tool %q not found in Base", name)
	return nil
}

// toolByName builds the base toolset over a workspace root and returns the named file tool.
func toolByName(t *testing.T, root, name string) agentkit.Tool {
	t.Helper()
	ts, err := tools.Base(tools.Config{Root: root})
	if err != nil {
		t.Fatalf("Base: %v", err)
	}
	for _, tl := range ts {
		if tl.Spec().Name == name {
			return tl
		}
	}
	t.Fatalf("tool %q not found in Base", name)
	return nil
}

func TestFile_RoundTrip(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	write := toolByName(t, root, "file_write")
	if _, err := write.Call(ctx, `{"path":"notes/a.txt","content":"hi"}`); err != nil {
		t.Fatalf("write: %v", err)
	}
	// The write landed inside the workspace, under the created parent dir.
	if b, err := os.ReadFile(filepath.Join(root, "notes", "a.txt")); err != nil || string(b) != "hi" {
		t.Fatalf("written file = %q err=%v", b, err)
	}

	read := toolByName(t, root, "file_read")
	if out, err := read.Call(ctx, `{"path":"notes/a.txt"}`); err != nil || out != "hi" {
		t.Fatalf("read = %q err=%v", out, err)
	}

	search := toolByName(t, root, "file_search")
	if out, err := search.Call(ctx, `{"pattern":"*.txt"}`); err != nil || !strings.Contains(out, "notes/a.txt") {
		t.Fatalf("search = %q err=%v", out, err)
	}

	move := toolByName(t, root, "file_move")
	if _, err := move.Call(ctx, `{"from":"notes/a.txt","to":"b.txt"}`); err != nil {
		t.Fatalf("move: %v", err)
	}
	remove := toolByName(t, root, "file_remove")
	if _, err := remove.Call(ctx, `{"path":"b.txt"}`); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("b.txt should be gone, stat err=%v", err)
	}
}

// TestFile_Escape is the load-bearing hardening test: no file tool may touch anything outside the
// workspace root — not via .., an absolute path, or a symlink inside the workspace pointing out (the
// case lexical confinement misses, now closed by os.Root).
func TestFile_Escape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Dir(root) // the temp root's parent — outside the workspace
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink INSIDE the workspace pointing OUT to the secret.
	if err := os.Symlink(secret, filepath.Join(root, "leak")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	ctx := context.Background()
	read := toolByName(t, root, "file_read")

	readCases := []struct {
		name, path string
		wantErr    bool
	}{
		{"parent-traversal", "../secret.txt", true},
		{"deep-traversal", "../../../../etc/passwd", true},
		{"symlink-out", "leak", true},
		{"absolute", secret, false}, // contained by Join into root/<abs>; not found, never the secret
	}
	for _, tc := range readCases {
		t.Run("read/"+tc.name, func(t *testing.T) {
			out, err := read.Call(ctx, `{"path":`+jsonQuote(tc.path)+`}`)
			if strings.Contains(out, "TOP SECRET") {
				t.Fatalf("LEAKED the secret via %q", tc.path)
			}
			if tc.wantErr && err == nil {
				t.Fatalf("escape %q was allowed, out=%q", tc.path, out)
			}
		})
	}

	// A write through a symlink pointing out must not modify the outside target.
	wlinkTarget := filepath.Join(outside, "target.txt")
	if err := os.WriteFile(wlinkTarget, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(wlinkTarget, filepath.Join(root, "wlink")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	write := toolByName(t, root, "file_write")
	if _, err := write.Call(ctx, `{"path":"wlink","content":"HACKED"}`); err == nil {
		t.Error("write through an escaping symlink was allowed")
	}
	if b, _ := os.ReadFile(wlinkTarget); string(b) != "original" {
		t.Fatalf("outside file was modified through a symlink: %q", b)
	}
	// And a plain .. write escape is refused.
	if _, err := write.Call(ctx, `{"path":"../evil.txt","content":"x"}`); err == nil {
		t.Error("write to ../evil.txt was allowed")
	}
}

// TestFile_MntConfinement is the HIGH-1 guarantee at the tools layer: when the file tools are rooted
// at the LLM mount (dir/mnt), the control-plane files that live as SIBLINGS of the mount (grants.json
// et al. at dir) are unreachable — not via .., not via an absolute path. Rooting the tools at dir
// itself (the old bug) would let file_read grants.json and file_write forge new grants.
func TestFile_MntConfinement(t *testing.T) {
	dir := t.TempDir()
	mnt := filepath.Join(dir, "mnt")
	if err := os.MkdirAll(mnt, 0o700); err != nil {
		t.Fatal(err)
	}
	// The control plane sits at dir, a sibling of the mount.
	if err := os.WriteFile(filepath.Join(dir, "grants.json"), []byte(`{"grants":"ALL"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	read := toolByName(t, mnt, "file_read")
	write := toolByName(t, mnt, "file_write")

	// Reads of the control plane must fail and never surface its contents.
	for _, p := range []string{"../grants.json", filepath.Join(dir, "grants.json")} {
		out, err := read.Call(ctx, `{"path":`+jsonQuote(p)+`}`)
		if strings.Contains(out, "ALL") {
			t.Fatalf("LEAKED grants.json via %q", p)
		}
		if err == nil && strings.Contains(out, "grants") {
			t.Fatalf("read of control plane %q was allowed: %q", p, out)
		}
	}

	// A write that tries to forge grants.json must not touch the real (sibling) file.
	if _, err := write.Call(ctx, `{"path":"../grants.json","content":"{\"grants\":\"PWNED\"}"}`); err == nil {
		t.Error("write to ../grants.json was allowed")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "grants.json")); strings.Contains(string(b), "PWNED") {
		t.Fatalf("control-plane grants.json was overwritten through the mount: %q", b)
	}
}

// TestFile_Read_RedactsVaultSecret is the MED-1 guarantee: a workspace file that itself holds a stored
// vault value is ingress-redacted on file_read, so the secret never reaches the model transcript.
func TestFile_Read_RedactsVaultSecret(t *testing.T) {
	root := t.TempDir()
	store := secret.NewStore()
	store.Set("api", []byte("SUPERSECRETVALUE123"))
	sc := secret.NewScanner(store)

	if err := os.WriteFile(filepath.Join(root, "leak.txt"), []byte("token=SUPERSECRETVALUE123 done"), 0o600); err != nil {
		t.Fatal(err)
	}
	read := fileToolWithScanner(t, root, "file_read", sc)
	out, err := read.Call(context.Background(), `{"path":"leak.txt"}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(out, "SUPERSECRETVALUE123") {
		t.Fatalf("file_read leaked a stored vault secret: %q", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("expected redaction marker, got %q", out)
	}
}

// jsonQuote quotes s as a JSON string literal.
func jsonQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}
