package chat_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/app/chat"
)

func userMsg(text string) agentkit.Message { return agentkit.Message{Role: agentkit.RoleUser, Content: text} }
func asstMsg(text string) agentkit.Message {
	return agentkit.Message{Role: agentkit.RoleAssistant, Content: text}
}

// metaByID returns the stored Meta for id, failing the test if it is absent.
func metaByID(t *testing.T, st *chat.Store, id string) chat.Meta {
	t.Helper()
	metas, err := st.Metas()
	if err != nil {
		t.Fatalf("Metas: %v", err)
	}
	for _, m := range metas {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("no meta for id %q", id)
	return chat.Meta{}
}

func TestValidID_Table(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"lowercase hex", "abc123", true},
		{"single digit", "0", true},
		{"deadbeef", "deadbeef", true},
		{"minted length", "0123456789ab", true},
		{"empty", "", false},
		{"uppercase", "ABC123", false},
		{"non-hex letter g", "g", false},
		{"dash separator", "ab-cd", false},
		{"path separator", "a/b", false},
		{"dot dot", "..", false},
		{"file-like", "a.json", false},
		{"spaces", "a b", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := chat.ValidID(tt.id); got != tt.want {
				t.Errorf("ValidID(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

func TestStore_Save_FirstSave_StampsMetaFromMessages(t *testing.T) {
	st, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "aa11bb"
	if err := st.Save(id, []agentkit.Message{userMsg("What is the weather"), asstMsg("Sunny")}); err != nil {
		t.Fatal(err)
	}
	m := metaByID(t, st, id)
	if m.Name != "What is the weather" {
		t.Errorf("Name = %q, want the first user line", m.Name)
	}
	if m.Source != chat.SourceUser {
		t.Errorf("Source = %q, want the store default %q", m.Source, chat.SourceUser)
	}
	if m.Created.IsZero() {
		t.Error("Created was not stamped on the first save")
	}
	if m.Turns != 1 {
		t.Errorf("Turns = %d, want 1", m.Turns)
	}
}

func TestStore_Save_BumpsTurnsAndUpdated_PreservesCreated(t *testing.T) {
	st, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "aa22bb"
	if err := st.Save(id, []agentkit.Message{userMsg("first")}); err != nil {
		t.Fatal(err)
	}
	m1 := metaByID(t, st, id)
	if err := st.Save(id, []agentkit.Message{userMsg("first"), asstMsg("a"), userMsg("second")}); err != nil {
		t.Fatal(err)
	}
	m2 := metaByID(t, st, id)

	if m2.Turns != 2 {
		t.Errorf("Turns after two saves = %d, want 2", m2.Turns)
	}
	if !m2.Created.Equal(m1.Created) {
		t.Errorf("Created changed on the second save: %v -> %v", m1.Created, m2.Created)
	}
	if m2.Updated.Before(m1.Updated) {
		t.Errorf("Updated went backwards: %v -> %v", m1.Updated, m2.Updated)
	}
}

// The serializer persists the meta it is given rather than re-deriving it: the Name fixed on the first
// save survives later saves whose first user message differs.
func TestStore_Save_DumbSerializer_PersistsGivenMetaVerbatim(t *testing.T) {
	st, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "aa33bb"
	if err := st.Save(id, []agentkit.Message{userMsg("original name")}); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(id, []agentkit.Message{userMsg("a different first message")}); err != nil {
		t.Fatal(err)
	}
	if got := metaByID(t, st, id).Name; got != "original name" {
		t.Errorf("Name = %q, want the first save's name preserved verbatim", got)
	}
}

func TestStore_Save_PreservesExistingTools(t *testing.T) {
	st, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "aa44bb"
	if err := st.Save(id, []agentkit.Message{userMsg("hi"), asstMsg("ok")}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendTools(id, []chat.ToolNode{{ID: 1, Tool: "probe"}}); err != nil {
		t.Fatal(err)
	}
	// A subsequent Save (read-modify-write of the same record) must not clobber the tool forest.
	if err := st.Save(id, []agentkit.Message{userMsg("hi"), asstMsg("ok"), userMsg("more")}); err != nil {
		t.Fatal(err)
	}
	groups, err := st.LoadTools(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0]) != 1 || groups[0][0].Tool != "probe" {
		t.Fatalf("tools after a later Save = %+v, want the earlier group preserved", groups)
	}
}

func TestStore_Save_WithSource_StampsSource(t *testing.T) {
	st, err := chat.NewStore(t.TempDir(), chat.WithSource(chat.SourceAgent))
	if err != nil {
		t.Fatal(err)
	}
	const id = "aa55bb"
	if err := st.Save(id, []agentkit.Message{userMsg("hi")}); err != nil {
		t.Fatal(err)
	}
	if got := metaByID(t, st, id).Source; got != chat.SourceAgent {
		t.Errorf("Source = %q, want %q", got, chat.SourceAgent)
	}
}

func TestStore_MarkRead_AdvancesReadToUpdated(t *testing.T) {
	st, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var c saveCounter
	st.OnSave(c.fn)

	const id = "bb11cc"
	if err := st.Save(id, []agentkit.Message{userMsg("hi")}); err != nil {
		t.Fatal(err)
	}
	if c.count() != 1 {
		t.Fatalf("OnSave after Save fired %d times, want 1", c.count())
	}
	if got := metaByID(t, st, id); !got.Read.IsZero() {
		t.Fatalf("Read before MarkRead = %v, want zero (unread)", got.Read)
	}

	if err := st.MarkRead(id); err != nil {
		t.Fatal(err)
	}
	m := metaByID(t, st, id)
	if !m.Read.Equal(m.Updated) {
		t.Errorf("Read = %v, want it advanced to Updated %v", m.Read, m.Updated)
	}
	if c.count() != 2 {
		t.Errorf("OnSave after MarkRead fired %d times total, want 2", c.count())
	}
}

// Marking an already-read chat changes nothing: no write, no broadcast (else a viewing client could
// echo the activity back into another MarkRead — a tight loop).
func TestStore_MarkRead_NoOpWhenAlreadyRead_NoWriteNoBroadcast(t *testing.T) {
	st, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var c saveCounter
	st.OnSave(c.fn)

	const id = "bb22cc"
	if err := st.Save(id, []agentkit.Message{userMsg("hi")}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkRead(id); err != nil { // first mark: advances, fires
		t.Fatal(err)
	}
	before := c.count()

	if err := st.MarkRead(id); err != nil { // second mark: already read → no-op
		t.Fatal(err)
	}
	if c.count() != before {
		t.Errorf("OnSave fired again on an already-read MarkRead (%d -> %d), want no broadcast", before, c.count())
	}
}

func TestStore_MarkRead_UnknownChat_NoOpNilErr(t *testing.T) {
	st, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var c saveCounter
	st.OnSave(c.fn)
	if err := st.MarkRead("abcdef"); err != nil {
		t.Fatalf("MarkRead of unknown chat = %v, want nil", err)
	}
	if c.count() != 0 {
		t.Errorf("OnSave fired %d times for an unknown chat, want 0", c.count())
	}
}

// The write is atomic (write temp, then rename) and 0600 — no .tmp file is left behind.
func TestStore_Write_ThenRename_Atomic_0600(t *testing.T) {
	dir := t.TempDir()
	st, err := chat.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	const id = "cc11dd"
	if err := st.Save(id, []agentkit.Message{userMsg("hi")}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file perm = %o, want 0600", perm)
	}
	tmps, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tmps) != 0 {
		t.Errorf("leftover temp files after rename: %v", tmps)
	}
}

func TestStore_LoadTools_NilWhenNone(t *testing.T) {
	st, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "cc22dd"
	if err := st.Save(id, []agentkit.Message{userMsg("hi")}); err != nil {
		t.Fatal(err)
	}
	groups, err := st.LoadTools(id)
	if err != nil {
		t.Fatal(err)
	}
	if groups != nil {
		t.Errorf("LoadTools with no tools saved = %+v, want nil", groups)
	}
}

func TestStore_Load_MissingChat_NilNoError(t *testing.T) {
	st, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := st.Load("abcdef")
	if err != nil {
		t.Fatalf("Load of a missing chat returned err %v, want nil", err)
	}
	if msgs != nil {
		t.Errorf("Load of a missing chat = %+v, want nil", msgs)
	}
}

// Every id-keyed path validates the id and rejects a bad one BEFORE touching the filesystem.
func TestStore_read_RejectsInvalidID_BeforeFS(t *testing.T) {
	dir := t.TempDir()
	st, err := chat.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	const bad = "BAD/id" // uppercase + separator — never a valid chat id

	paths := map[string]func() error{
		"Load":      func() error { _, e := st.Load(bad); return e },
		"LoadTools": func() error { _, e := st.LoadTools(bad); return e },
		"Save":      func() error { return st.Save(bad, []agentkit.Message{userMsg("x")}) },
		"MarkRead":  func() error { return st.MarkRead(bad) },
		"Delete":    func() error { return st.Delete(bad) },
	}
	for name, fn := range paths {
		t.Run(name, func(t *testing.T) {
			if err := fn(); !errors.Is(err, chat.ErrInvalidID) {
				t.Errorf("%s(%q) = %v, want ErrInvalidID", name, bad, err)
			}
		})
	}

	// Nothing reached the filesystem.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("invalid-id paths wrote to disk: %v", entries)
	}
}

func TestStore_Delete_MissingNotError_InvalidRejected(t *testing.T) {
	st, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Delete("abcdef"); err != nil { // valid id, no such file → not an error
		t.Errorf("Delete of a missing chat = %v, want nil", err)
	}
	if err := st.Delete("NOPE"); !errors.Is(err, chat.ErrInvalidID) { // invalid id → rejected
		t.Errorf("Delete of an invalid id = %v, want ErrInvalidID", err)
	}
}

func TestStore_Metas_SortedByUpdatedDescending_NeverNil(t *testing.T) {
	st, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// Empty store: never nil.
	metas, err := st.Metas()
	if err != nil {
		t.Fatal(err)
	}
	if metas == nil {
		t.Fatal("Metas on an empty store = nil, want an empty non-nil slice")
	}
	if len(metas) != 0 {
		t.Fatalf("Metas on an empty store = %d entries, want 0", len(metas))
	}

	// Saved in order aa, bb, cc — cc is the most recently updated.
	for _, id := range []string{"aa", "bb", "cc"} {
		if err := st.Save(id, []agentkit.Message{userMsg(id)}); err != nil {
			t.Fatal(err)
		}
	}
	metas, err = st.Metas()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 3 {
		t.Fatalf("Metas = %d entries, want 3", len(metas))
	}
	for i := 1; i < len(metas); i++ {
		if metas[i-1].Updated.Before(metas[i].Updated) {
			t.Errorf("Metas not sorted by Updated descending: %v before %v", metas[i-1].Updated, metas[i].Updated)
		}
	}
	if metas[0].ID != "cc" {
		t.Errorf("first meta id = %q, want the most recently updated (cc)", metas[0].ID)
	}
}

func TestStore_NameFrom_FirstUserLine_TrimmedToLimit(t *testing.T) {
	st, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "dd11ee"
	long := strings.Repeat("x", 100)
	if err := st.Save(id, []agentkit.Message{userMsg(long + "\nsecond line")}); err != nil {
		t.Fatal(err)
	}
	name := metaByID(t, st, id).Name
	runes := []rune(name)
	if len(runes) != 41 { // 40 kept + the ellipsis
		t.Fatalf("Name len = %d runes, want 40 + ellipsis", len(runes))
	}
	if !strings.HasSuffix(name, "…") {
		t.Errorf("Name = %q, want it ellipsised", name)
	}
	if strings.Contains(name, "\n") {
		t.Error("Name kept content past the first line")
	}
}

func TestStore_PreviewFrom_LastUserOrAssistantFirstLine(t *testing.T) {
	st, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "dd22ee"
	msgs := []agentkit.Message{
		userMsg("first question"),
		asstMsg("the final answer\nwith a second line"),
	}
	if err := st.Save(id, msgs); err != nil {
		t.Fatal(err)
	}
	if got := metaByID(t, st, id).Preview; got != "the final answer" {
		t.Errorf("Preview = %q, want the last message's first line", got)
	}
}

func TestStore_NameFrom_MultibyteRuneBoundary(t *testing.T) {
	st, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "dd33ee"
	// 50 multibyte runes — trimming must land on a rune boundary, never mid-byte.
	if err := st.Save(id, []agentkit.Message{userMsg(strings.Repeat("あ", 50))}); err != nil {
		t.Fatal(err)
	}
	name := metaByID(t, st, id).Name
	if !utf8ValidAllRunes(name) {
		t.Fatalf("Name %q contains an invalid rune — trimmed mid-byte", name)
	}
	if runes := []rune(name); len(runes) != 41 { // 40 + ellipsis
		t.Fatalf("Name len = %d runes, want 40 + ellipsis", len(runes))
	}
}

func utf8ValidAllRunes(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func TestStore_Rename_SetsName_UnknownNoOp(t *testing.T) {
	st, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "ee11ff"
	if err := st.Save(id, []agentkit.Message{userMsg("original")}); err != nil {
		t.Fatal(err)
	}
	if err := st.Rename(id, "renamed"); err != nil {
		t.Fatal(err)
	}
	if got := metaByID(t, st, id).Name; got != "renamed" {
		t.Errorf("Name = %q, want %q", got, "renamed")
	}
	if err := st.Rename("abcdef", "x"); err != nil { // unknown chat → no-op
		t.Errorf("Rename of unknown chat = %v, want nil", err)
	}
}

// OnSave registration races the Save/fireSaved read of the callback; the store guards it with its
// lock, so this stays race-clean under -race.
func TestStore_OnSave_GuardedConcurrent(t *testing.T) {
	st, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var c saveCounter
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 50 {
			st.OnSave(c.fn)
		}
	})
	wg.Go(func() {
		for i := range 50 {
			_ = st.Save("abc123", []agentkit.Message{userMsg("m")})
			_ = i
		}
	})
	wg.Wait()
}

func TestStore_AppendTools_DoesNotBumpMetaOrFire(t *testing.T) {
	st, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var c saveCounter
	st.OnSave(c.fn)

	const id = "ff11aa"
	if err := st.Save(id, []agentkit.Message{userMsg("hi"), asstMsg("ok")}); err != nil {
		t.Fatal(err)
	}
	m1 := metaByID(t, st, id)
	firesAfterSave := c.count()

	if err := st.AppendTools(id, []chat.ToolNode{{ID: 1, Tool: "probe"}}); err != nil {
		t.Fatal(err)
	}
	m2 := metaByID(t, st, id)

	if m2.Turns != m1.Turns {
		t.Errorf("AppendTools bumped Turns %d -> %d", m1.Turns, m2.Turns)
	}
	if !m2.Updated.Equal(m1.Updated) {
		t.Errorf("AppendTools bumped Updated %v -> %v", m1.Updated, m2.Updated)
	}
	if c.count() != firesAfterSave {
		t.Errorf("AppendTools fired OnSave (%d -> %d), want silent", firesAfterSave, c.count())
	}
}

// Metas skips a file it cannot decode (corrupt) or whose name is not a valid id, and still returns the
// good entries.
func TestStore_Corrupt_UnreadableFile_Metas_SkipsEntry(t *testing.T) {
	dir := t.TempDir()
	st, err := chat.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	const good = "ab12cd"
	if err := st.Save(good, []agentkit.Message{userMsg("hi")}); err != nil {
		t.Fatal(err)
	}
	// A corrupt (undecodable) file and an invalid-named file, both alongside the good one.
	if err := os.WriteFile(filepath.Join(dir, "beef99.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "NOTHEX.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	metas, err := st.Metas()
	if err != nil {
		t.Fatalf("Metas returned an error instead of skipping bad files: %v", err)
	}
	if len(metas) != 1 || metas[0].ID != good {
		t.Fatalf("Metas = %+v, want only the one good entry %q", metas, good)
	}
}
