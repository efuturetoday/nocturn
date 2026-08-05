package knowledge

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
)

// indexVersion is the on-disk format. A file written by a newer nocturn is refused rather than
// misread — the alternative is a field silently defaulting to zero somewhere inside a vector.
const indexVersion = 1

// Index is everything known about a workspace's documents: which files were seen, what they hashed
// to, and the passages and vectors they produced.
//
// It lives OUTSIDE the mount, beside grants.json. The corpus is data the model may read and write;
// the index is host state — hashes, offsets, vectors — and a model that could edit it could point a
// search result at text that is not in the file.
//
// Model and Dims are recorded because a vector only means something relative to the model that
// produced it. Two different lengths cannot be compared at all, which fails loudly. Two different
// models at the SAME length is the dangerous case: the arithmetic still works and returns confident
// nonsense. Both are refused by Compatible.
type Index struct {
	Version int    `json:"version"`
	Model   string `json:"model"`
	Dims    int    `json:"dims"`
	// Files is keyed by the mount-relative, forward-slashed path.
	Files map[string]*FileEntry `json:"files"`
}

// FileEntry is one document as it was when last indexed.
type FileEntry struct {
	// SHA256 of the file's bytes. The whole basis of incremental indexing: an unchanged hash means
	// the chunks and vectors below are still exactly what this file produces, so re-embedding it
	// would spend the operator's money to arrive at what is already on disk.
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`
	// ModTime is the file's timestamp when it was last read, in Unix nanoseconds. Only ever a HINT:
	// size and timestamp both matching means the file is not read at all, which is what makes a
	// periodic reconcile cost a directory walk instead of hashing the whole corpus. The hash stays
	// the truth, and a file whose timestamp moved is re-read and re-hashed before anything is
	// re-embedded — so a touched-but-unchanged file costs a read, never a provider call.
	ModTime int64         `json:"modTime"`
	Chunks  []StoredChunk `json:"chunks"`
}

// StoredChunk is a passage and its vector.
type StoredChunk struct {
	Chunk
	// Vector is base64 of little-endian float32. Not a JSON array of numbers: at 768 dimensions that
	// is roughly 10 KB of decimal text per chunk against 4 KB here, and a corpus of a few thousand
	// chunks turns the difference into tens of megabytes that have to be parsed on every load.
	Vector string `json:"vector"`
}

// newIndex is an empty index for a given embedder.
func newIndex(model string, dims int) *Index {
	return &Index{Version: indexVersion, Model: model, Dims: dims, Files: map[string]*FileEntry{}}
}

// Compatible reports whether this index can answer questions embedded by that model at that length.
//
// Both halves matter and only one of them is obvious. A length mismatch makes comparison
// arithmetically impossible. A model mismatch at the same length does not — it produces a number,
// and the number is meaningless, which is the failure that would never be noticed.
func (ix *Index) Compatible(model string, dims int) bool {
	return ix.Model == model && ix.Dims == dims
}

// Chunks returns every stored passage, in a stable order: by path, then by position in the file.
// Deterministic because search ranks on scores that tie, and a result list that reshuffles between
// two identical queries reads as instability that is not there.
func (ix *Index) Chunks() []StoredChunk {
	var out []StoredChunk
	for _, p := range slices.Sorted(maps.Keys(ix.Files)) {
		out = append(out, ix.Files[p].Chunks...)
	}
	return out
}

// Stats is what a status command reports.
func (ix *Index) Stats() (files, chunks int) {
	for _, f := range ix.Files {
		chunks += len(f.Chunks)
	}
	return len(ix.Files), chunks
}

// loadIndex reads the index at path.
//
// A missing file is an empty index: a workspace nobody has indexed yet is the normal starting
// state, not an error. A corrupt one IS an error — silently starting from nothing would re-embed
// the whole corpus at the operator's expense and look like it merely took a while.
func loadIndex(path, model string, dims int) (*Index, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return newIndex(model, dims), nil
	}
	if err != nil {
		return nil, fmt.Errorf("knowledge: reading %s: %w", path, err)
	}

	var ix Index
	if err := json.Unmarshal(raw, &ix); err != nil {
		return nil, fmt.Errorf("knowledge: %s is not valid: %w", path, err)
	}
	if ix.Version != indexVersion {
		return nil, fmt.Errorf("knowledge: %s was written by a different version (%d, this is %d) — delete it and index again",
			path, ix.Version, indexVersion)
	}
	if ix.Files == nil {
		ix.Files = map[string]*FileEntry{}
	}
	return &ix, nil
}

// save writes the index atomically, 0600.
//
// Write-then-rename because a half-written index that replaced a good one loses the whole corpus to
// a power cut, and rebuilding it is not free — it re-embeds every document. 0600 because the index
// holds the documents' text, which is as private as the documents.
func (ix *Index) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(ix)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// encodeVector packs a vector as base64 of little-endian float32.
func encodeVector(v []float32) string {
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(f))
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// decodeVector unpacks what encodeVector wrote, and reports a length that is not a whole number of
// float32s rather than returning a truncated vector.
func decodeVector(s string) ([]float32, error) {
	buf, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("knowledge: vector is not base64: %w", err)
	}
	if len(buf)%4 != 0 {
		return nil, fmt.Errorf("knowledge: vector is %d bytes, not a whole number of float32", len(buf))
	}
	out := make([]float32, len(buf)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[4*i:]))
	}
	return out, nil
}
