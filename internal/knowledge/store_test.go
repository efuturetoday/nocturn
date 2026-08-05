package knowledge

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEmbedder is deterministic and counts what it was asked to do, which is what the incremental
// tests are really asserting on: not "is the index right" but "was the provider paid twice".
type fakeEmbedder struct {
	model string
	dims  int

	// Guarded: the watch tests read and write these from the test goroutine while Watch runs its own.
	mu     sync.Mutex
	texts  int    // texts embedded in total, which is what the incremental tests watch
	failOn string // a text containing this makes the whole batch fail
}

// embedded is how many texts the provider has been asked for so far.
func (f *fakeEmbedder) embedded() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.texts
}

// setFailOn makes any batch containing sub fail; empty makes the provider healthy again.
func (f *fakeEmbedder) setFailOn(sub string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failOn = sub
}

func (f *fakeEmbedder) Model() string { return f.model }
func (f *fakeEmbedder) Dims() int     { return f.dims }

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.mu.Lock()
	failOn := f.failOn
	f.texts += len(texts)
	f.mu.Unlock()

	out := make([][]float32, len(texts))
	for i, t := range texts {
		if failOn != "" && strings.Contains(t, failOn) {
			return nil, os.ErrPermission
		}
		// A vector that depends on the text, so two different passages are two different vectors and
		// the same passage is always the same one.
		v := make([]float32, f.dims)
		for j := range v {
			v[j] = float32((len(t)+j*7+int(t[j%len(t)]))%97) / 97
		}
		out[i] = v
	}
	return out, nil
}

// storeFixture builds a store over a fresh corpus folder, with the index outside it — the same
// arrangement the workspace uses.
func storeFixture(t *testing.T, emb Embedder) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	corpus := filepath.Join(root, "mnt", "knowledge")
	if err := os.MkdirAll(corpus, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Dir: corpus, IndexPath: filepath.Join(root, "knowledge.idx.json"), Embedder: emb, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, corpus
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStore_IndexesTheFolder(t *testing.T) {
	emb := &fakeEmbedder{model: "m", dims: 8}
	s, corpus := storeFixture(t, emb)
	write(t, corpus, "notes.md", "# Alpha\n\nabout alpha\n\n# Beta\n\nabout beta\n")
	write(t, corpus, "sub/deeper.md", "# Gamma\n\nabout gamma\n")

	rep, err := s.Index(t.Context())
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if rep.Indexed != 2 || rep.Unchanged != 0 {
		t.Errorf("report = %+v, want 2 indexed", rep)
	}
	if rep.Chunks != 3 {
		t.Errorf("chunks = %d, want 3", rep.Chunks)
	}
	paths, err := s.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(paths, []string{"notes.md", "sub/deeper.md"}) {
		t.Errorf("paths = %v", paths)
	}
}

// The whole basis of incremental indexing: unchanged bytes must not be re-embedded, because that
// spends the operator's money to arrive at what is already on disk.
func TestStore_UnchangedFilesAreNotReEmbedded(t *testing.T) {
	emb := &fakeEmbedder{model: "m", dims: 8}
	s, corpus := storeFixture(t, emb)
	write(t, corpus, "a.md", "# A\n\nfirst\n")
	write(t, corpus, "b.md", "# B\n\nsecond\n")

	if _, err := s.Index(t.Context()); err != nil {
		t.Fatalf("first Index: %v", err)
	}
	afterFirst := emb.embedded()

	// A fresh store, so nothing is cached in memory and the index on disk is what is consulted.
	s2, err := New(Options{Dir: s.Dir(), IndexPath: s.IndexPath(), Embedder: emb})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := s2.Index(t.Context())
	if err != nil {
		t.Fatalf("second Index: %v", err)
	}
	if rep.Unchanged != 2 || rep.Indexed != 0 {
		t.Errorf("report = %+v, want 2 unchanged and nothing indexed", rep)
	}
	if emb.embedded() != afterFirst {
		t.Errorf("%d texts embedded on the second run, want 0 more than %d", emb.embedded()-afterFirst, afterFirst)
	}

	// Changing one file re-embeds that one and only that one.
	write(t, corpus, "a.md", "# A\n\nfirst, corrected\n")
	rep, err = s2.Index(t.Context())
	if err != nil {
		t.Fatalf("third Index: %v", err)
	}
	if rep.Indexed != 1 || rep.Unchanged != 1 {
		t.Errorf("report = %+v, want exactly the edited file re-indexed", rep)
	}
}

func TestStore_DeletedFilesLeaveTheIndex(t *testing.T) {
	emb := &fakeEmbedder{model: "m", dims: 8}
	s, corpus := storeFixture(t, emb)
	write(t, corpus, "keep.md", "# K\n\nkept\n")
	write(t, corpus, "gone.md", "# G\n\ngoing\n")
	if _, err := s.Index(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(corpus, "gone.md")); err != nil {
		t.Fatal(err)
	}
	rep, err := s.Index(t.Context())
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if rep.Removed != 1 {
		t.Errorf("removed = %d, want 1", rep.Removed)
	}
	paths, _ := s.Paths()
	if slices.Contains(paths, "gone.md") {
		t.Error("a deleted file is still in the index")
	}
}

// Silently ignoring a file is how somebody drops a PDF in the folder and never learns it is not
// searchable. Naming it is the whole handling.
func TestStore_UnreadableFormatsAreNamedNotIgnored(t *testing.T) {
	emb := &fakeEmbedder{model: "m", dims: 8}
	s, corpus := storeFixture(t, emb)
	write(t, corpus, "notes.md", "# N\n\nprose\n")
	write(t, corpus, "manual.pdf", "%PDF-1.7 binary-ish")
	write(t, corpus, "photo.png", "\x89PNG")
	write(t, corpus, ".hidden.md", "# H\n\neditor state\n")

	rep, err := s.Index(t.Context())
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if !slices.Equal(rep.Skipped, []string{"manual.pdf", "photo.png"}) {
		t.Errorf("skipped = %v, want the two unreadable formats named", rep.Skipped)
	}
	paths, _ := s.Paths()
	if !slices.Equal(paths, []string{"notes.md"}) {
		t.Errorf("paths = %v, want only the Markdown file", paths)
	}
}

// The failure that would otherwise never be noticed: another model at the same length still produces
// numbers, and the numbers are meaningless.
func TestStore_RefusesAnIndexFromAnotherEmbedder(t *testing.T) {
	s, corpus := storeFixture(t, &fakeEmbedder{model: "first-model", dims: 8})
	write(t, corpus, "a.md", "# A\n\ntext\n")
	if _, err := s.Index(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Run("a different model at the same length", func(t *testing.T) {
		other, err := New(Options{Dir: s.Dir(), IndexPath: s.IndexPath(), Embedder: &fakeEmbedder{model: "second-model", dims: 8}})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := other.Stats(); err == nil {
			t.Fatal("an index from another model was accepted")
		}
		_, _, err = other.Stats()
		if !strings.Contains(err.Error(), "first-model") || !strings.Contains(err.Error(), "second-model") {
			t.Errorf("error = %q, want it to name both models", err)
		}
		if !strings.Contains(err.Error(), s.IndexPath()) {
			t.Errorf("error = %q, want it to name the file to delete", err)
		}
	})

	t.Run("the same model at a different length", func(t *testing.T) {
		other, err := New(Options{Dir: s.Dir(), IndexPath: s.IndexPath(), Embedder: &fakeEmbedder{model: "first-model", dims: 16}})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := other.Stats(); err == nil {
			t.Fatal("an index at another length was accepted")
		}
	})
}

// A corrupt index must not read as "nothing is indexed": starting from nothing would re-embed the
// whole corpus at the operator's expense and look like it merely took a while.
func TestStore_CorruptIndexIsAnError(t *testing.T) {
	s, _ := storeFixture(t, &fakeEmbedder{model: "m", dims: 8})
	if err := os.WriteFile(s.IndexPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Stats(); err == nil {
		t.Fatal("a corrupt index returned no error")
	}
}

// A workspace nobody has indexed yet is the normal starting state.
func TestStore_MissingFolderAndIndexAreEmpty(t *testing.T) {
	root := t.TempDir()
	s, err := New(Options{
		Dir:       filepath.Join(root, "mnt", "knowledge"),
		IndexPath: filepath.Join(root, "knowledge.idx.json"),
		Embedder:  &fakeEmbedder{model: "m", dims: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := s.Index(t.Context())
	if err != nil {
		t.Fatalf("Index over a missing folder: %v", err)
	}
	if rep.Indexed != 0 || rep.Chunks != 0 {
		t.Errorf("report = %+v, want an empty run", rep)
	}
}

func TestStore_NoEmbedderIsRefused(t *testing.T) {
	if _, err := New(Options{Dir: "a", IndexPath: "b"}); err == nil {
		t.Fatal("a store was built without an embedder")
	}
}

// Vectors are stored normalized so a search is a dot product rather than a division per comparison.
func TestStore_StoredVectorsAreUnitLength(t *testing.T) {
	s, corpus := storeFixture(t, &fakeEmbedder{model: "m", dims: 8})
	write(t, corpus, "a.md", "# A\n\nsome text to embed\n")
	if _, err := s.Index(t.Context()); err != nil {
		t.Fatal(err)
	}

	ix, err := s.load()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range ix.Chunks() {
		v, err := decodeVector(c.Vector)
		if err != nil {
			t.Fatalf("decodeVector: %v", err)
		}
		if got := dot(v, v); got < 0.999 || got > 1.001 {
			t.Errorf("vector·itself = %v, want 1 (unit length)", got)
		}
	}
}

// A provider that fails partway must not leave a half-built index behind claiming to be current:
// the next run would treat the files it did reach as unchanged and never come back for the rest.
func TestStore_EmbeddingFailureLeavesNoPartialIndex(t *testing.T) {
	emb := &fakeEmbedder{model: "m", dims: 8, failOn: "poison"}
	s, corpus := storeFixture(t, emb)
	write(t, corpus, "a.md", "# A\n\nfine text\n")
	write(t, corpus, "b-poison.md", "# B\n\nthis passage contains poison\n")

	if _, err := s.Index(t.Context()); err == nil {
		t.Fatal("a failing provider was reported as a successful run")
	}
	if _, err := os.Stat(s.IndexPath()); !os.IsNotExist(err) {
		t.Errorf("an index was written despite the run failing (stat err = %v)", err)
	}

	// With the provider healthy again, the whole corpus is indexed — nothing was silently marked done.
	emb.setFailOn("")
	rep, err := s.Index(t.Context())
	if err != nil {
		t.Fatalf("Index after recovery: %v", err)
	}
	if rep.Indexed != 2 {
		t.Errorf("report = %+v, want both files indexed after the earlier failure", rep)
	}
}

// The point of the timestamp hint: a reconcile over an unchanged corpus opens nothing and writes
// nothing. Without this a ticker would hash every byte of the corpus every interval, and rewrite a
// possibly large index file to report that nothing happened.
func TestStore_UnchangedReconcileReadsAndWritesNothing(t *testing.T) {
	emb := &fakeEmbedder{model: "m", dims: 8}
	s, corpus := storeFixture(t, emb)
	write(t, corpus, "a.md", "# A\n\nsome text\n")
	if _, err := s.Index(t.Context()); err != nil {
		t.Fatal(err)
	}

	before, err := os.Stat(s.IndexPath())
	if err != nil {
		t.Fatal(err)
	}
	embedsBefore := emb.embedded()

	// A second store, so the decision is made against the index on disk rather than a warm cache.
	s2, err := New(Options{Dir: s.Dir(), IndexPath: s.IndexPath(), Embedder: emb})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := s2.Index(t.Context())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rep.Unchanged != 1 || rep.Indexed != 0 {
		t.Errorf("report = %+v, want one unchanged file", rep)
	}
	if emb.embedded() != embedsBefore {
		t.Errorf("the provider was called again on an unchanged corpus")
	}

	after, err := os.Stat(s.IndexPath())
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("the index file was rewritten although nothing changed")
	}
}

// A file whose timestamp moved but whose bytes did not costs a read, never a provider call — and
// the new timestamp is recorded so the next reconcile takes the cheap path again.
func TestStore_TouchedButUnchangedFileIsNotReEmbedded(t *testing.T) {
	emb := &fakeEmbedder{model: "m", dims: 8}
	s, corpus := storeFixture(t, emb)
	write(t, corpus, "a.md", "# A\n\nunchanged text\n")
	if _, err := s.Index(t.Context()); err != nil {
		t.Fatal(err)
	}
	embedsBefore := emb.embedded()

	// Same bytes, new timestamp.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(filepath.Join(corpus, "a.md"), future, future); err != nil {
		t.Fatal(err)
	}
	rep, err := s.Index(t.Context())
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if rep.Indexed != 0 || rep.Unchanged != 1 {
		t.Errorf("report = %+v, want the touched file recognised as unchanged", rep)
	}
	if emb.embedded() != embedsBefore {
		t.Error("a touched but unchanged file was re-embedded")
	}
}
