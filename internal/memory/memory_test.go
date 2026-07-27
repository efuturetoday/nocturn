package memory_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/memory"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// newStore builds a Store over a fresh temp dir, returning the dir and the tools keyed by name.
func newStore(t *testing.T, sc *secret.Scanner) (string, map[string]agentkit.Tool) {
	t.Helper()
	dir := t.TempDir()
	s := memory.New(dir, sc)
	ts, err := s.Tools()
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	byName := make(map[string]agentkit.Tool, len(ts))
	for _, tool := range ts {
		byName[tool.Spec().Name] = tool
	}
	for _, want := range []string{"memory_read", "memory_write"} {
		if _, ok := byName[want]; !ok {
			t.Fatalf("missing tool %q", want)
		}
	}
	return dir, byName
}

// logged builds a Store whose warnings land in buf.
func logged(t *testing.T, dir string) (*memory.Store, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	s := memory.New(dir, nil)
	s.SetLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	return s, &buf
}

// writeNote lays a note down directly on disk, bypassing the tool.
func writeNote(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// approver records what it was asked and answers with a fixed verdict.
type approver struct {
	approve bool
	asked   int
	last    gate.Action
	suggest []gate.Grant
}

func (a *approver) Ask(_ context.Context, act gate.Action, suggest []gate.Grant) (bool, gate.Grant, gate.Recall, error) {
	a.asked++
	a.last = act
	a.suggest = suggest
	return a.approve, gate.Grant{Kind: act.Kind, Target: act.Target}, gate.RecallSession, nil
}

// gated installs gate machinery that asks appr for every memory action, mirroring the policy an
// unattended agent run uses. RecallNever keeps every call reaching the approver so a test can count.
func gated(appr gate.Approver) context.Context {
	p := gate.PolicyFunc(func(a gate.Action) gate.Ruling {
		if a.Kind == memory.Kind {
			return gate.AskWith(gate.RecallNever)
		}
		return gate.Allowed()
	})
	return gate.With(context.Background(), p, gate.NewMemGrants(), appr)
}

func writeArgs(t *testing.T, path, summary, content string) string {
	t.Helper()
	b, err := json.Marshal(struct {
		Path    string `json:"path"`
		Summary string `json:"summary"`
		Content string `json:"content"`
	}{path, summary, content})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func readArgs(t *testing.T, path string) string {
	t.Helper()
	b, err := json.Marshal(struct {
		Path string `json:"path"`
	}{path})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestIndex_EmptyCostsNothing: a workspace that never stored anything must add no prompt tokens, and
// a missing folder must not be an error.
func TestIndex_EmptyCostsNothing(t *testing.T) {
	for _, tc := range []struct{ name, dir string }{
		{"missing folder", filepath.Join(t.TempDir(), "nope")},
		{"empty folder", t.TempDir()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := memory.New(tc.dir, nil).Index(); got != "" {
				t.Fatalf("Index = %q, want empty", got)
			}
		})
	}
}

// TestIndex_DerivedFromTheNotes is the core of the design: the catalog is not a file anyone
// maintains, it is a view of what is actually on disk — so it cannot drift, and deleting a note
// deletes its line.
func TestIndex_DerivedFromTheNotes(t *testing.T) {
	dir := t.TempDir()
	s := memory.New(dir, nil)

	writeNote(t, dir, "people/lina.md", "---\ndescription: daughter, 7 years old\n---\nLikes dinosaurs.\n")
	writeNote(t, dir, "prefs/coding.md", "---\ndescription: Go, no comments in code\n---\n")

	got := s.Index()
	for _, want := range []string{"people/lina.md — daughter, 7 years old", "prefs/coding.md — Go, no comments in code"} {
		if !strings.Contains(got, want) {
			t.Errorf("catalog missing %q; got:\n%s", want, got)
		}
	}
	// Sorted by path, so the prompt is stable turn to turn (and cacheable).
	if i, j := strings.Index(got, "people/"), strings.Index(got, "prefs/"); i > j {
		t.Errorf("catalog is not sorted by path:\n%s", got)
	}

	// Delete the note and the line goes with it — no bookkeeping step, no stale entry.
	if err := os.Remove(filepath.Join(dir, "prefs", "coding.md")); err != nil {
		t.Fatal(err)
	}
	if got := s.Index(); strings.Contains(got, "prefs/coding.md") {
		t.Errorf("a deleted note still appears in the catalog:\n%s", got)
	}
}

// TestIndex_SummaryFallbacks: a hand-written note without frontmatter must still show up. A note the
// model cannot summarize is worse than useless if it silently vanishes — the assistant would believe
// it never stored the fact.
func TestIndex_SummaryFallbacks(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "a.md", "---\ndescription: from frontmatter\n---\n# Heading\nbody\n")
	writeNote(t, dir, "b.md", "# Just a heading\n\nbody text\n")
	writeNote(t, dir, "c.md", "plain first line\nsecond line\n")
	writeNote(t, dir, "d.md", "\n\n\n")
	writeNote(t, dir, "e.txt", "not markdown, must be ignored\n")

	got := memory.New(dir, nil).Index()
	for _, want := range []string{
		"a.md — from frontmatter",
		"b.md — Just a heading",
		"c.md — plain first line",
		"d.md — (no summary)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("catalog missing %q; got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "e.txt") {
		t.Errorf("a non-Markdown file leaked into the catalog:\n%s", got)
	}
}

// TestIndex_ReadsFreshEveryCall: the human may correct a note in an editor between two turns, so the
// catalog must not cache.
func TestIndex_ReadsFreshEveryCall(t *testing.T) {
	dir := t.TempDir()
	s := memory.New(dir, nil)

	writeNote(t, dir, "n.md", "---\ndescription: first\n---\n")
	if got := s.Index(); !strings.Contains(got, "first") {
		t.Fatalf("Index = %q, want the first description", got)
	}
	writeNote(t, dir, "n.md", "---\ndescription: second\n---\n")
	if got := s.Index(); !strings.Contains(got, "second") || strings.Contains(got, "first") {
		t.Fatalf("after edit Index = %q, want the second description", got)
	}
}

// TestIndex_CapIsEnforcedAndStated: the budget is the whole reason the catalog is derived — it can
// be enforced rather than requested. A full memory must read as full, never as complete.
func TestIndex_CapIsEnforcedAndStated(t *testing.T) {
	dir := t.TempDir()
	const n = 200
	for i := range n {
		writeNote(t, dir, fmt.Sprintf("notes/n%03d.md", i),
			fmt.Sprintf("---\ndescription: Präferenz Nummer %d, größer geschrieben\n---\n", i))
	}

	got := memory.New(dir, nil).Index()
	if len(got) > 4<<10 {
		t.Errorf("catalog is %d bytes, want it held near its budget", len(got))
	}
	if !utf8.ValidString(got) {
		t.Error("catalog is not valid UTF-8")
	}
	if !strings.Contains(got, "more notes not listed") {
		t.Errorf("the cap was silent; catalog tail = %q", got[max(0, len(got)-120):])
	}
	// Whole lines only — a truncated entry would point at a path that does not exist.
	for line := range strings.SplitSeq(got, "\n") {
		if strings.HasPrefix(line, "notes/") && !strings.Contains(line, " — ") {
			t.Errorf("catalog holds a half-written entry: %q", line)
		}
	}
}

// TestIndex_LongSummaryClipped: one rambling note must not crowd every other fact out of the prompt.
func TestIndex_LongSummaryClipped(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "long.md", "---\ndescription: "+strings.Repeat("wortreich ", 80)+"\n---\n")
	writeNote(t, dir, "short.md", "---\ndescription: kurz\n---\n")

	got := memory.New(dir, nil).Index()
	if !strings.Contains(got, "short.md — kurz") {
		t.Errorf("a long note squeezed out a short one:\n%s", got)
	}
	for line := range strings.SplitSeq(got, "\n") {
		if len([]rune(line)) > 200 {
			t.Errorf("catalog line is %d runes, want it clipped: %q", len([]rune(line)), line)
		}
	}
}

// TestIndex_UnreadableNoteIsReported: a note that EXISTS but cannot be read must leave a trace. Its
// silent absence would have the assistant act, with full confidence, as though the fact was never
// stored.
func TestIndex_UnreadableNoteIsReported(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "ok.md", "---\ndescription: fine\n---\n")
	broken := filepath.Join(dir, "broken.md")
	writeNote(t, dir, "broken.md", "unreadable\n")
	if err := os.Chmod(broken, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(broken, 0o600) }) // so TempDir cleanup can remove it
	if _, err := os.ReadFile(broken); err == nil {
		t.Skip("cannot make a file unreadable here (running as root?)")
	}
	s, buf := logged(t, dir)

	if got := s.Index(); !strings.Contains(got, "ok.md") {
		t.Errorf("one bad note took the whole catalog down: %q", got)
	}
	if !strings.Contains(buf.String(), "broken.md") {
		t.Errorf("an unreadable note passed silently; log = %q", buf.String())
	}
}

// TestIndex_EmptyFolderIsSilent: the ordinary empty case runs every turn and must not log.
func TestIndex_EmptyFolderIsSilent(t *testing.T) {
	s, buf := logged(t, t.TempDir())
	if got := s.Index(); got != "" {
		t.Errorf("Index = %q, want empty", got)
	}
	if buf.Len() != 0 {
		t.Errorf("an empty memory logged %q, want silence — this runs every turn", buf.String())
	}
}

// TestWrite_ToolOwnsTheFileFormat: the model supplies text, never YAML. A summary carrying a colon,
// a quote or a newline must survive the round trip — hand-built frontmatter is exactly where that
// would break.
func TestWrite_ToolOwnsTheFileFormat(t *testing.T) {
	dir, tls := newStore(t, nil)
	const summary = `note: "quoted", and: more`
	appr := &approver{approve: true}

	if _, err := tls["memory_write"].Call(gated(appr), writeArgs(t, "tricky.md", summary, "# Body\ntext\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "tricky.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "---\n") {
		t.Fatalf("the tool did not write a frontmatter block:\n%s", raw)
	}
	if !strings.Contains(string(raw), "# Body") {
		t.Errorf("the body is missing:\n%s", raw)
	}
	if got := memory.New(dir, nil).Index(); !strings.Contains(got, summary) {
		t.Errorf("summary did not survive the round trip; catalog = %q", got)
	}
}

// TestWrite_SummaryRequired: a note without a catalog line would be invisible in the prompt, so the
// tool refuses it rather than storing something the assistant will never be reminded of.
func TestWrite_SummaryRequired(t *testing.T) {
	dir, tls := newStore(t, nil)
	appr := &approver{approve: true}

	if _, err := tls["memory_write"].Call(gated(appr), writeArgs(t, "n.md", "   ", "body")); err == nil {
		t.Fatal("a blank summary was accepted")
	}
	if appr.asked != 0 {
		t.Errorf("approver asked %d times for a malformed call, want 0", appr.asked)
	}
	if _, err := os.Stat(filepath.Join(dir, "n.md")); !errors.Is(err, os.ErrNotExist) {
		t.Error("the rejected note was written anyway")
	}
}

// TestWrite_CreatesParentsAndReplacesWhole: memory_write is a whole-file replacement that may create
// nested folders, and leaves no temp file behind.
func TestWrite_CreatesParentsAndReplacesWhole(t *testing.T) {
	dir, tls := newStore(t, nil)
	ctx := gated(&approver{approve: true})

	if _, err := tls["memory_write"].Call(ctx, writeArgs(t, "people/lina.md", "daughter, 7", "age 7\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := tls["memory_write"].Call(ctx, writeArgs(t, "people/lina.md", "daughter, 8", "age 8\n")); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "people", "lina.md"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(got), "age 7") {
		t.Errorf("the rewrite appended instead of replacing:\n%s", got)
	}
	// The write goes through a temp file; a successful rename must leave none behind.
	entries, err := os.ReadDir(filepath.Join(dir, "people"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("left a temp file behind: %s", e.Name())
		}
	}
}

// TestWrite_RejectsNonMarkdown: memory must stay editable in any text editor, which is the whole
// reason it is plain files.
func TestWrite_RejectsNonMarkdown(t *testing.T) {
	dir, tls := newStore(t, nil)
	appr := &approver{approve: true}

	if _, err := tls["memory_write"].Call(gated(appr), writeArgs(t, "notes.txt", "s", "hi")); err == nil {
		t.Fatal("writing a .txt succeeded, want rejection")
	}
	if appr.asked != 0 {
		t.Errorf("approver asked %d times for a rejected extension, want 0", appr.asked)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file exists after a rejected write")
	}
}

// TestWrite_DeclinedApprovalWritesNothing: the gate is the fence. A declined ask must leave the disk
// untouched — not a partial file, not a temp file.
func TestWrite_DeclinedApprovalWritesNothing(t *testing.T) {
	dir, tls := newStore(t, nil)
	appr := &approver{approve: false}

	_, err := tls["memory_write"].Call(gated(appr), writeArgs(t, "people/lina.md", "daughter", "age 7"))
	if !errors.Is(err, gate.ErrDeniedDeclined) {
		t.Fatalf("declined write error = %v, want ErrDeniedDeclined", err)
	}
	if appr.asked != 1 {
		t.Fatalf("approver asked %d times, want 1", appr.asked)
	}
	if _, err := os.Stat(filepath.Join(dir, "people")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a declined write created %s", filepath.Join(dir, "people"))
	}
}

// TestWrite_UnattendedIsDeniedClosed: a background agent with no reachable human must not be able to
// write memory — the missing approver means denied, never allowed.
func TestWrite_UnattendedIsDeniedClosed(t *testing.T) {
	dir, tls := newStore(t, nil)

	_, err := tls["memory_write"].Call(gated(nil), writeArgs(t, "people/lina.md", "daughter", "age 7"))
	if !errors.Is(err, gate.ErrDeniedUnattended) {
		t.Fatalf("unattended write error = %v, want ErrDeniedUnattended", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "people")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an unattended write touched the disk")
	}
}

// TestWrite_SuggestsDirectoryWidening: the human gets one widening beyond the exact path, and never
// one at the root — a root grant would not be a step, it would be the whole store.
func TestWrite_SuggestsDirectoryWidening(t *testing.T) {
	_, tls := newStore(t, nil)

	nested := &approver{approve: true}
	if _, err := tls["memory_write"].Call(gated(nested), writeArgs(t, "people/lina.md", "s", "x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(nested.suggest) != 1 || nested.suggest[0].Target != "people/*" || nested.suggest[0].Kind != memory.Kind {
		t.Fatalf("suggestions = %+v, want one {memory, people/*}", nested.suggest)
	}

	root := &approver{approve: true}
	if _, err := tls["memory_write"].Call(gated(root), writeArgs(t, "note.md", "s", "x")); err != nil {
		t.Fatalf("write at root: %v", err)
	}
	if len(root.suggest) != 0 {
		t.Fatalf("root suggestions = %+v, want none", root.suggest)
	}
}

// TestWrite_SecretBlockedBeforeTheGate: a vault secret must never reach memory — it would then sit
// in every single prompt. It must also never be PUT to the human, who might approve it.
func TestWrite_SecretBlockedBeforeTheGate(t *testing.T) {
	const val = "supersecretvalue"
	store := secret.NewStore()
	store.Set("token", []byte(val))

	dir, tls := newStore(t, secret.NewScanner(store))

	for _, tc := range []struct{ name, summary, content string }{
		{"in the content", "harmless", "the token is " + val},
		{"in the summary", "token " + val, "harmless"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			appr := &approver{approve: true}
			_, err := tls["memory_write"].Call(gated(appr), writeArgs(t, "creds.md", tc.summary, tc.content))
			if err == nil {
				t.Fatal("writing a vault secret into memory succeeded, want blocked")
			}
			if strings.Contains(err.Error(), val) {
				t.Fatalf("error string leaked the secret: %q", err)
			}
			if appr.asked != 0 {
				t.Errorf("approver asked %d times, want 0 — a secret must not be offered for approval", appr.asked)
			}
			if _, statErr := os.Stat(filepath.Join(dir, "creds.md")); !errors.Is(statErr, os.ErrNotExist) {
				t.Error("the blocked content was written anyway")
			}
		})
	}
}

// TestRead_ReturnsNoteUngated: reading back one's own notes is context ingestion, zero authority — it
// must work with no gate machinery installed at all.
func TestRead_ReturnsNoteUngated(t *testing.T) {
	dir, tls := newStore(t, nil)
	writeNote(t, dir, "people/lina.md", "---\ndescription: daughter\n---\nage 7\n")

	got, err := tls["memory_read"].Call(context.Background(), readArgs(t, "people/lina.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(got, "age 7") {
		t.Fatalf("read = %q, want the note body", got)
	}
}

// TestRead_RedactsStoredSecret: a note could hold a value that later became a vault secret; strip it
// on the way to the model.
func TestRead_RedactsStoredSecret(t *testing.T) {
	const val = "supersecretvalue"
	store := secret.NewStore()
	store.Set("token", []byte(val))

	dir, tls := newStore(t, secret.NewScanner(store))
	writeNote(t, dir, "old.md", "token: "+val+"\n")

	got, err := tls["memory_read"].Call(context.Background(), readArgs(t, "old.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(got, val) {
		t.Fatalf("read returned the raw secret: %q", got)
	}
}

// TestRead_NonRegularFileRejected: a directory (or device node) must not be readable as a note.
func TestRead_NonRegularFileRejected(t *testing.T) {
	dir, tls := newStore(t, nil)
	if err := os.MkdirAll(filepath.Join(dir, "people"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := tls["memory_read"].Call(context.Background(), readArgs(t, "people")); err == nil {
		t.Fatal("reading a directory succeeded, want rejection")
	}
}

// TestConfinement_EscapeIsLogged: a rejected path is a security-relevant event, so it leaves a trace
// like the file tools' does — more so here, since past this folder lie grants, vault and persona.
func TestConfinement_EscapeIsLogged(t *testing.T) {
	s, buf := logged(t, t.TempDir())
	ts, err := s.Tools()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range ts {
		if tool.Spec().Name != "memory_read" {
			continue
		}
		if _, err := tool.Call(context.Background(), readArgs(t, "../grants.json")); err == nil {
			t.Fatal("escaping read succeeded")
		}
	}
	if !strings.Contains(buf.String(), "escapes") {
		t.Errorf("a rejected escape left no trace; log = %q", buf.String())
	}
}

// TestConfinement_EscapesRejected is the load-bearing test: the memory folder is control plane, and
// neither tool may become a probe into the workspace root (grants.json, vault.enc, PERSONA.md).
//
// The property under test is "nothing outside the folder is read or written" — NOT "every attempt
// returns an error". os.Root draws that line, and it draws it in two different shapes: an escaping
// path errors, while a rename onto a symlinked FILE succeeds and merely replaces the link entry,
// leaving its target untouched. Asserting the error shape would test os.Root's spelling; asserting
// the outside file tests the boundary. So both are checked, each where it applies.
func TestConfinement_EscapesRejected(t *testing.T) {
	outside := t.TempDir()
	secretFile := filepath.Join(outside, "grants.json")
	const original = `[{"kind":"net","target":"*"}]`
	if err := os.WriteFile(secretFile, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(outside, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}

	dir, tls := newStore(t, nil)
	// A symlinked FILE and a symlinked DIRECTORY inside the folder, both pointing out. The directory
	// is the one that would actually escape if os.Root did not re-validate intermediate components.
	if err := os.Symlink(secretFile, filepath.Join(dir, "escape.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "dir"), filepath.Join(dir, "escapedir")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	for _, tc := range []struct {
		name, path string
		errors     bool // whether os.Root rejects it outright, vs. containing it another way
	}{
		{"traversal", "../grants.json", true},
		{"deep traversal", "../../etc/passwd", true},
		{"absolute", secretFile, true},
		{"through a symlinked dir", "escapedir/pwned.md", true},
		{"onto a symlinked file", "escape.md", false}, // contained: the link entry is replaced
	} {
		t.Run("read/"+tc.name, func(t *testing.T) {
			out, err := tls["memory_read"].Call(context.Background(), readArgs(t, tc.path))
			if err == nil {
				t.Fatalf("read %q succeeded, returning %q — confinement breached", tc.path, out)
			}
		})
		t.Run("write/"+tc.name, func(t *testing.T) {
			appr := &approver{approve: true}
			_, err := tls["memory_write"].Call(gated(appr), writeArgs(t, tc.path, "s", "pwned"))
			if tc.errors && err == nil {
				t.Fatalf("write %q succeeded — confinement breached", tc.path)
			}
		})
	}

	// Whatever the error shapes were, nothing outside the folder may have changed.
	if _, err := os.Stat(filepath.Join(outside, "dir", "pwned.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a write landed outside the memory folder, through a symlinked directory")
	}
	got, err := os.ReadFile(secretFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("the file outside the memory folder was modified: %q", got)
	}
}
