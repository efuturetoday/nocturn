package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/efuturetoday/nocturn/internal/secret"
)

const (
	// maxFileBytes is the largest document read at all. Past this the file is almost never prose,
	// and chunking it would spend a long time producing passages nobody searches for.
	maxFileBytes = 8 << 20

	// maxChunks bounds a whole corpus. Not a performance limit — a linear scan over a hundred
	// thousand vectors is still fast — but a cost one: every chunk past this is money spent
	// embedding a folder somebody probably did not mean to point at.
	maxChunks = 50_000
)

// Store is one workspace's document index: the folder, the index beside it, and the embedder that
// connects them.
type Store struct {
	dir      string // the corpus, inside the mount
	indexAt  string // the index, outside it
	embedder Embedder
	scanner  *secret.Scanner
	readers  []Reader
	log      *slog.Logger

	mu    sync.Mutex // one indexing run at a time; searches read under it too
	ix    *Index
	dirty bool // the cached index has changes the disk does not — see Index
}

// Options is what a Store needs. A struct rather than five parameters, and it is the seam where the
// two paths differ: Dir is inside the mount because documents are data, IndexPath is outside it
// because the index is host state.
type Options struct {
	// Dir is the corpus folder, mnt/knowledge in a workspace.
	Dir string
	// IndexPath is where the index is written, outside the mount.
	IndexPath string
	// Embedder is required — see New.
	Embedder Embedder
	// Scanner redacts vault values out of search results. Nil when no vault is unlocked.
	Scanner *secret.Scanner
	// Log is optional.
	Log *slog.Logger
}

// New opens the store. It reads neither the folder nor the index — Index and Search do.
//
// A nil embedder is refused: without one there is nothing this type can do, and a Store that
// silently answers nothing is worse than a missing one. Whether the feature exists at all is the
// caller's decision, the same shape as the whoami tool and a speaker model.
func New(o Options) (*Store, error) {
	if o.Embedder == nil {
		return nil, fmt.Errorf("knowledge: no embedder configured")
	}
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Store{
		dir:      o.Dir,
		indexAt:  o.IndexPath,
		embedder: o.Embedder,
		scanner:  o.Scanner,
		readers:  []Reader{MarkdownReader{}},
		log:      log,
	}, nil
}

// SetReaders replaces the set of document readers. The Markdown reader is the default; a consumer
// that has an extractor for another format installs it here, before the first Index.
//
// Not a With* option: those configure at construction and this mutates afterwards, and calling the
// two the same thing is how somebody expects one and gets the other.
func (s *Store) SetReaders(rs ...Reader) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readers = slices.Clone(rs)
}

// load reads the index once, and refuses one built by a different embedder.
//
// The refusal is the whole point of recording the model: an index answering with vectors from
// another model still produces numbers, and the numbers mean nothing. Saying so is the only way
// that failure is ever noticed. The caller holds the lock.
func (s *Store) load() (*Index, error) {
	if s.ix != nil {
		return s.ix, nil
	}
	ix, err := loadIndex(s.indexAt, s.embedder.Model(), s.embedder.Dims())
	if err != nil {
		return nil, err
	}
	if files, _ := ix.Stats(); files > 0 && !ix.Compatible(s.embedder.Model(), s.embedder.Dims()) {
		return nil, fmt.Errorf(
			"knowledge: the index was built with %s at %d dimensions, but this daemon embeds with %s at %d — "+
				"delete %s and index again",
			ix.Model, ix.Dims, s.embedder.Model(), s.embedder.Dims(), s.indexAt)
	}
	s.ix = ix
	return ix, nil
}

// Report is what one indexing run did.
type Report struct {
	Indexed   int      // files embedded this run
	Unchanged int      // files whose hash matched, so their vectors were reused
	Removed   int      // files gone from disk, dropped from the index
	Chunks    int      // passages in the index afterwards
	Skipped   []string // files no reader handles, named rather than silently ignored
}

// Index brings the index in line with the folder.
//
// Incremental by content hash: a file whose bytes are unchanged keeps the chunks and vectors it
// already has, because re-embedding it would spend the operator's money to arrive at what is
// already on disk. A file that is gone is dropped; a file no reader handles is SKIPPED AND NAMED —
// silently ignoring it is how somebody drops a PDF in the folder and never learns it is not
// searchable.
func (s *Store) Index(ctx context.Context) (Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// A run mutates the cached index as it goes, and only the save at the end makes that real. If it
	// does not get there, the cache is ahead of the disk — and the next run would skip the files this
	// one embedded but never persisted, leaving the index permanently short of the corpus with
	// nothing reporting it. Dropping the cache costs one file read and removes the whole failure.
	defer func() {
		if s.dirty {
			s.ix = nil
			s.dirty = false
		}
	}()

	ix, err := s.load()
	if err != nil {
		return Report{}, err
	}
	// A run against a different embedder rebuilds rather than mixing: load already refused a
	// populated mismatch, so reaching here with one means the index is empty.
	ix.Model, ix.Dims = s.embedder.Model(), s.embedder.Dims()

	found, rep, err := s.walk()
	if err != nil {
		return Report{}, err
	}

	// Anything the walk did not see is gone from disk.
	for p := range ix.Files {
		if _, ok := found[p]; !ok {
			s.dirty = true
			delete(ix.Files, p)
			rep.Removed++
		}
	}

	_, held := ix.Stats()
	for _, p := range slices.Sorted(maps.Keys(found)) {
		doc := found[p]
		if prev, ok := ix.Files[p]; ok && prev.SHA256 == doc.sum {
			rep.Unchanged++
			continue
		}

		chunks, err := s.split(p, doc)
		if err != nil {
			return rep, err
		}
		// Checked BEFORE embedding, not after. The ceiling exists to stop somebody pointing this at a
		// whole source tree, and a limit reported once the provider has already been paid stops
		// nothing — it only says so afterwards.
		replacing := len(previousChunks(ix, p))
		if held-replacing+len(chunks) > maxChunks {
			return rep, fmt.Errorf(
				"knowledge: %s would take the index past %d passages — point the folder at documents rather than at a whole tree",
				p, maxChunks)
		}

		stored, err := s.embed(ctx, p, chunks)
		if err != nil {
			return rep, err
		}
		s.dirty = true
		held += len(stored) - replacing
		ix.Files[p] = &FileEntry{SHA256: doc.sum, Size: doc.size, Chunks: stored}
		rep.Indexed++
		s.log.Debug("knowledge: indexed", "path", p, "chunks", len(stored))
	}

	_, rep.Chunks = ix.Stats()
	if err := ix.save(s.indexAt); err != nil {
		return rep, err
	}
	s.dirty = false // the cache and the disk agree again
	return rep, nil
}

// document is a file the walk accepted.
type document struct {
	data []byte
	sum  string
	size int
	ext  string
}

// walk collects every readable document under the corpus folder.
func (s *Store) walk() (map[string]document, Report, error) {
	out := map[string]document{}
	var rep Report

	root, err := os.OpenRoot(s.dir)
	if os.IsNotExist(err) {
		return out, rep, nil // no folder yet is an empty corpus, not a failure
	}
	if err != nil {
		return nil, rep, fmt.Errorf("knowledge: opening %s: %w", s.dir, err)
	}
	defer root.Close()

	err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil // an unreadable directory is skipped, not fatal
		}
		if strings.HasPrefix(path.Base(rel), ".") {
			return nil // dotfiles are somebody's editor state, not their documents
		}

		ext := strings.ToLower(path.Ext(rel))
		if s.readerFor(ext) == nil {
			rep.Skipped = append(rep.Skipped, rel)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() > maxFileBytes {
			rep.Skipped = append(rep.Skipped, rel)
			s.log.Warn("knowledge: file is too large to index", "path", rel, "bytes", info.Size())
			return nil
		}

		data, err := fs.ReadFile(root.FS(), rel)
		if err != nil {
			s.log.Warn("knowledge: could not read", "path", rel, "err", err)
			return nil
		}
		sum := sha256.Sum256(data)
		out[rel] = document{data: data, sum: hex.EncodeToString(sum[:]), size: len(data), ext: ext}
		return nil
	})
	if err != nil {
		return nil, rep, fmt.Errorf("knowledge: walking %s: %w", s.dir, err)
	}
	slices.Sort(rep.Skipped)
	return out, rep, nil
}

// readerFor is the first reader that takes this extension, or nil.
func (s *Store) readerFor(ext string) Reader {
	for _, r := range s.readers {
		if r.Handles(ext) {
			return r
		}
	}
	return nil
}

// previousChunks is what this path contributed to the index before this run, for the running total.
func previousChunks(ix *Index, path string) []StoredChunk {
	if f, ok := ix.Files[path]; ok {
		return f.Chunks
	}
	return nil
}

// split reads one file into passages, without embedding anything. Separate from embed so how much a
// document will cost is known before it is paid for.
func (s *Store) split(rel string, doc document) ([]Chunk, error) {
	secs, err := s.readerFor(doc.ext).Sections(doc.data)
	if err != nil {
		return nil, fmt.Errorf("knowledge: reading %s: %w", rel, err)
	}
	return chunkSections(rel, secs), nil
}

// embed turns passages into stored chunks.
func (s *Store) embed(ctx context.Context, rel string, chunks []Chunk) ([]StoredChunk, error) {
	if len(chunks) == 0 {
		return nil, nil
	}

	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.embedText()
	}
	vecs, err := s.embedder.Embed(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("knowledge: embedding %s: %w", rel, err)
	}
	if len(vecs) != len(chunks) {
		return nil, fmt.Errorf("knowledge: %s: %d passages, %d vectors", rel, len(chunks), len(vecs))
	}

	out := make([]StoredChunk, len(chunks))
	for i, c := range chunks {
		out[i] = StoredChunk{Chunk: c, Vector: encodeVector(normalize(vecs[i]))}
	}
	return out, nil
}

// Stats reports what the index holds, without indexing.
func (s *Store) Stats() (files, chunks int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ix, err := s.load()
	if err != nil {
		return 0, 0, err
	}
	files, chunks = ix.Stats()
	return files, chunks, nil
}

// Paths lists the indexed documents, sorted.
func (s *Store) Paths() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ix, err := s.load()
	if err != nil {
		return nil, err
	}
	return slices.Sorted(maps.Keys(ix.Files)), nil
}

// Dir is the corpus folder, for a caller that has to create or report it.
func (s *Store) Dir() string { return s.dir }

// IndexPath is where the index is written, so an error message can name it.
func (s *Store) IndexPath() string { return s.indexAt }
