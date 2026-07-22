package tools_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
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

// denyFile is a policy that denies every FileKind mutation but allows everything else — used to prove
// the read-vs-write gate split (reads run under it; writes are refused).
func denyFile(ctx context.Context) context.Context {
	return gate.With(ctx, gate.PolicyFunc(func(a gate.Action) gate.Ruling {
		if a.Kind == tools.FileKind {
			return gate.Denied()
		}
		return gate.Allowed()
	}), nil, nil)
}

// TestFile_Write_GatedOnFileKind_NotWrittenOnDeny is the mutation-gate guarantee: a denied file write
// returns an error AND leaves no file behind — the effect runs only after the gate says yes. The gated
// Target is the workspace-relative path.
func TestFile_Write_GatedOnFileKind_NotWrittenOnDeny(t *testing.T) {
	root := t.TempDir()
	var seen []gate.Action
	ctx := capturePolicy(context.Background(), &seen, func(gate.Action) gate.Ruling { return gate.Denied() })

	write := toolByName(t, root, "file_write")
	if _, err := write.Call(ctx, `{"path":"notes/a.txt","content":"hi"}`); err == nil {
		t.Fatal("denied write returned no error")
	}
	if _, err := os.Stat(filepath.Join(root, "notes", "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("file was written despite the gate denial (stat err=%v)", err)
	}
	if len(seen) != 1 || seen[0].Kind != tools.FileKind || seen[0].Target != "notes/a.txt" {
		t.Fatalf("write gated on wrong action: %+v", seen)
	}
}

// TestFile_Read_Ungated proves the read-vs-write split: read/list/stat/search are observations that
// run even under a policy that denies every FileKind mutation — they never touch the gate.
func TestFile_Read_Ungated(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := denyFile(context.Background())

	if out, err := toolByName(t, root, "file_read").Call(ctx, `{"path":"sub/a.txt"}`); err != nil || out != "hello" {
		t.Fatalf("read blocked by a file-deny policy: out=%q err=%v", out, err)
	}
	if _, err := toolByName(t, root, "file_list").Call(ctx, `{"path":"sub"}`); err != nil {
		t.Fatalf("list blocked by a file-deny policy: %v", err)
	}
	if out, err := toolByName(t, root, "file_stat").Call(ctx, `{"path":"sub/a.txt"}`); err != nil || !strings.Contains(out, `"exists":true`) {
		t.Fatalf("stat blocked by a file-deny policy: out=%q err=%v", out, err)
	}
	if out, err := toolByName(t, root, "file_search").Call(ctx, `{"pattern":"*.txt"}`); err != nil || !strings.Contains(out, "sub/a.txt") {
		t.Fatalf("search blocked by a file-deny policy: out=%q err=%v", out, err)
	}
}

// TestFile_Remove_GatedOnFileKind proves remove is a gated mutation: a denial refuses it and the file
// survives.
func TestFile_Remove_GatedOnFileKind(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var seen []gate.Action
	ctx := capturePolicy(context.Background(), &seen, func(gate.Action) gate.Ruling { return gate.Denied() })

	remove := toolByName(t, root, "file_remove")
	if _, err := remove.Call(ctx, `{"path":"keep.txt"}`); err == nil {
		t.Fatal("denied remove returned no error")
	}
	if _, err := os.Stat(filepath.Join(root, "keep.txt")); err != nil {
		t.Fatalf("file was removed despite the gate denial: %v", err)
	}
	if len(seen) != 1 || seen[0].Kind != tools.FileKind || seen[0].Target != "keep.txt" {
		t.Fatalf("remove gated on wrong action: %+v", seen)
	}
}

// TestFile_Move_GatedOnDest proves move is gated on the DESTINATION (the write), and both endpoints
// are confined — a denial leaves the source in place.
func TestFile_Move_GatedOnDest(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "from.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var seen []gate.Action
	ctx := capturePolicy(context.Background(), &seen, func(gate.Action) gate.Ruling { return gate.Denied() })

	move := toolByName(t, root, "file_move")
	if _, err := move.Call(ctx, `{"from":"from.txt","to":"dir/to.txt"}`); err == nil {
		t.Fatal("denied move returned no error")
	}
	if _, err := os.Stat(filepath.Join(root, "from.txt")); err != nil {
		t.Fatalf("source vanished despite the denied move: %v", err)
	}
	if len(seen) != 1 || seen[0].Target != "dir/to.txt" {
		t.Fatalf("move should gate on the destination, got %+v", seen)
	}
}

// TestFile_Confine_RejectsBeforeGate proves confinement is a pre-gate check: an escaping path is
// refused before the gate is ever consulted, so the human is never asked to approve an escape.
func TestFile_Confine_RejectsBeforeGate(t *testing.T) {
	root := t.TempDir()
	var seen []gate.Action
	ctx := capturePolicy(context.Background(), &seen, func(gate.Action) gate.Ruling { return gate.Allowed() })

	write := toolByName(t, root, "file_write")
	if _, err := write.Call(ctx, `{"path":"../evil.txt","content":"x"}`); err == nil {
		t.Fatal("escaping write was allowed")
	}
	if len(seen) != 0 {
		t.Fatalf("the gate was consulted for an escaping path (should reject first): %+v", seen)
	}
}

// TestFile_PathMatch_GlobDoesNotCrossSlash proves a standing "notes/*" grant covers a direct child but
// NOT a nested one — path.Match's "*" does not cross "/". The nested write has no grant and, with no
// approver, is denied.
func TestFile_PathMatch_GlobDoesNotCrossSlash(t *testing.T) {
	root := t.TempDir()
	// Ask-everything policy + a standing grant for the notes directory; no approver (unattended).
	ask := gate.PolicyFunc(func(gate.Action) gate.Ruling { return gate.AskWith(gate.RecallSession) })
	grants := gate.NewMemGrants(gate.Grant{Kind: tools.FileKind, Target: "notes/*"})
	ctx := gate.With(context.Background(), ask, grants, nil)

	write := toolByName(t, root, "file_write")
	if _, err := write.Call(ctx, `{"path":"notes/todo.md","content":"x"}`); err != nil {
		t.Fatalf("grant notes/* should cover notes/todo.md: %v", err)
	}
	if _, err := write.Call(ctx, `{"path":"notes/sub/deep.md","content":"x"}`); err == nil {
		t.Fatal("grant notes/* must NOT cover notes/sub/deep.md (glob does not cross slash)")
	}
}

// TestFile_DirSuggestions_ContainingDir proves the widening a human is offered on a file write is the
// containing directory ("notes/*"); a write at the workspace root offers none.
func TestFile_DirSuggestions_ContainingDir(t *testing.T) {
	root := t.TempDir()
	var suggested []gate.Grant
	approver := approverFunc(func(_ context.Context, _ gate.Action, s []gate.Grant) (bool, gate.Grant, gate.Recall, error) {
		suggested = s
		return true, gate.Grant{Kind: tools.FileKind, Target: "notes/todo.md"}, gate.RecallSession, nil
	})
	ask := gate.PolicyFunc(func(gate.Action) gate.Ruling { return gate.AskWith(gate.RecallSession) })

	write := toolByName(t, root, "file_write")
	ctx := gate.With(context.Background(), ask, gate.NewMemGrants(), approver)
	if _, err := write.Call(ctx, `{"path":"notes/todo.md","content":"x"}`); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(suggested) != 1 || suggested[0].Target != "notes/*" {
		t.Fatalf("containing-dir widening = %+v, want [{file notes/*}]", suggested)
	}

	suggested = nil
	if _, err := write.Call(ctx, `{"path":"root.md","content":"x"}`); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(suggested) != 0 {
		t.Fatalf("a root-level file should offer no widening, got %+v", suggested)
	}
}

// TestFile_Search covers the search edges: a slashless pattern matches names at any depth, a bad glob
// is a clear error, and a capped sweep is flagged rather than read as complete.
func TestFile_Search(t *testing.T) {
	root := t.TempDir()

	t.Run("slashless matches name at any depth", func(t *testing.T) {
		if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "a", "b", "deep.md"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := toolByName(t, root, "file_search").Call(context.Background(), `{"pattern":"*.md"}`)
		if err != nil || !strings.Contains(out, "a/b/deep.md") {
			t.Fatalf("slashless search missed a deep file: out=%q err=%v", out, err)
		}
	})

	t.Run("bad pattern is a clear error", func(t *testing.T) {
		_, err := toolByName(t, root, "file_search").Call(context.Background(), `{"pattern":"[unterminated"}`)
		if err == nil || !strings.Contains(err.Error(), "invalid pattern") {
			t.Fatalf("bad glob not clearly reported: %v", err)
		}
	})

	t.Run("truncation flagged", func(t *testing.T) {
		big := t.TempDir()
		for i := 0; i < 550; i++ {
			if err := os.WriteFile(filepath.Join(big, "f"+strconv.Itoa(i)+".txt"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		out, err := toolByName(t, big, "file_search").Call(context.Background(), `{"pattern":"*.txt"}`)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if !strings.Contains(out, "truncated at 500") {
			t.Fatalf("capped sweep not flagged: %q", out[max(0, len(out)-80):])
		}
	})
}

// TestFile_Read_CappedAt1MiB proves a large file read is capped at 1 MiB so it can't blow up context.
func TestFile_Read_CappedAt1MiB(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "big.txt"), make([]byte, 2<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := toolByName(t, root, "file_read").Call(context.Background(), `{"path":"big.txt"}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(out) != 1<<20 {
		t.Fatalf("read cap not enforced: got %d bytes, want %d", len(out), 1<<20)
	}
}

// TestFile_Stat_MissingPath_ExistsFalse proves stat of a missing path is {"exists":false}, not an
// error — the model can probe before writing.
func TestFile_Stat_MissingPath_ExistsFalse(t *testing.T) {
	root := t.TempDir()
	out, err := toolByName(t, root, "file_stat").Call(context.Background(), `{"path":"nope.txt"}`)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !strings.Contains(out, `"exists":false`) {
		t.Fatalf("missing path stat = %q, want exists:false", out)
	}
}

// TestFile_Write_CreatesParentsPerms proves a write creates missing parent directories (0700) and
// writes the file 0600.
func TestFile_Write_CreatesParentsPerms(t *testing.T) {
	root := t.TempDir()
	write := toolByName(t, root, "file_write")
	if _, err := write.Call(context.Background(), `{"path":"a/b/c.txt","content":"hi"}`); err != nil {
		t.Fatalf("write: %v", err)
	}
	dirInfo, err := os.Stat(filepath.Join(root, "a"))
	if err != nil {
		t.Fatalf("parent dir not created: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("parent dir perms = %o, want 0700", perm)
	}
	fileInfo, err := os.Stat(filepath.Join(root, "a", "b", "c.txt"))
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file perms = %o, want 0600", perm)
	}
}

// TestFile_List_DefaultRoot proves file_list with no path lists the workspace root.
func TestFile_List_DefaultRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "top.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := toolByName(t, root, "file_list").Call(context.Background(), `{}`)
	if err != nil || !strings.Contains(out, "top.txt") {
		t.Fatalf("default-root list = %q err=%v", out, err)
	}
}

// TestFile_AbsolutePath_Contained documents a confinement subtlety worth pinning: an absolute path is
// not REJECTED by confine (contrary to a naive reading) — filepath.Join folds it under the root, so it
// is contained and simply not found, never reaching the real /etc/passwd. os.Root is the real boundary.
func TestFile_AbsolutePath_Contained(t *testing.T) {
	root := t.TempDir()
	out, err := toolByName(t, root, "file_read").Call(context.Background(), `{"path":"/etc/passwd"}`)
	if strings.Contains(out, "root:") {
		t.Fatalf("absolute path escaped confinement and read the real /etc/passwd: %q", out)
	}
	// It is contained (folded under root), so the read fails as not-found rather than leaking.
	if err == nil {
		t.Fatalf("expected a not-found error for the contained absolute path, got out=%q", out)
	}
}

// approverFunc adapts a func to gate.Approver.
type approverFunc func(context.Context, gate.Action, []gate.Grant) (bool, gate.Grant, gate.Recall, error)

func (f approverFunc) Ask(ctx context.Context, a gate.Action, s []gate.Grant) (bool, gate.Grant, gate.Recall, error) {
	return f(ctx, a, s)
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
