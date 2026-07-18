// store.go is the persistent store for a workspace's named chats — the "several chats with
// saved history" the companion app shows. Each chat is one file under <ws>/chats/
// (control-plane, outside mnt/): its metadata plus the conversation messages. The Store is a
// self-contained STATE service (its own synchronization, atomic writes), like grantstore:
// callers deal in metadata + messages, never the file layout. A saved chat carries no
// authority; every effect a reopened chat performs still passes the broker + HITL.
package chat

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/internal/brain"
)

// ErrInvalidID rejects a chat id that isn't a server-minted lowercase-hex token (empty,
// path separators, ".."). Returned instead of a silent no-op so a caller can tell a
// rejected id from an unknown-but-valid one — the mutation never touches the filesystem.
var ErrInvalidID = errors.New("chat: invalid id")

// Origin is who created a chat — a one-time provenance marker, distinct from who drives a
// given turn (that is session.Source). A client filters/groups by it.
type Origin string

const (
	// OriginUser is a chat a human started (TUI launch, Ctrl+N, the app's "new chat").
	OriginUser Origin = "user"
	// OriginAgent is a chat a scheduled/child-agent run inhabits (grouped as "agent activity").
	OriginAgent Origin = "agent"
)

// Meta is one chat's summary, for the picker (no messages).
type Meta struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Origin  Origin    `json:"origin"`
	Agent   string    `json:"agent,omitempty"` // the agent that owns this run ("" = a root/user chat) — a saved record knows which charter rebuilds it
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	Turns   int       `json:"turns"` // user messages, for a "N messages" hint
}

// record is the on-disk shape: metadata plus the full conversation and its tool forest.
type record struct {
	Meta
	Messages []brain.Message `json:"messages"`
	Forest   []ToolFrame     `json:"forest,omitempty"` // the completed tool call tree, for faithful reload
}

// Store owns one workspace's chats under dir. Safe for concurrent use (several app clients).
type Store struct {
	dir string
	mu  sync.Mutex
}

// LoadStore returns a store over <ws>/chats (created if missing).
func LoadStore(dir string) *Store {
	_ = os.MkdirAll(dir, 0o755)
	return &Store{dir: dir}
}

// List returns every chat's metadata, most-recently-updated first. Unreadable/!json files
// are skipped (fail-safe — a corrupt chat never breaks the listing).
func (s *Store) List() []Meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var metas []Meta
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		if rec, ok := s.readLocked(chatIDFromFile(e.Name())); ok {
			metas = append(metas, rec.Meta)
		}
	}
	slices.SortFunc(metas, func(a, b Meta) int { return b.Updated.Compare(a.Updated) })
	return metas
}

// NewID mints a fresh, filesystem-safe chat id WITHOUT writing anything. The Manager mints
// a chat in memory and lets the first save create the file — so a chat that never takes a
// turn leaves no empty file behind (lazy-persist).
func (s *Store) NewID() string { return newID() }

// Load returns a chat's messages, tool forest, and metadata. ok is false for an unknown or
// invalid id.
func (s *Store) Load(id string) ([]brain.Message, []ToolFrame, Meta, bool) {
	if !validID(id) {
		return nil, nil, Meta{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.readLocked(id)
	if !ok {
		return nil, nil, Meta{}, false
	}
	return rec.Messages, rec.Forest, rec.Meta, true
}

// Save persists a chat's current messages under its metadata (updating Updated and
// the turn count), preserving the on-disk Created time. meta's Name/Origin/Agent
// override only when non-empty. The first Save of a memory-minted chat creates its
// file (lazy-persist). Returns ErrInvalidID for an id that isn't server-minted
// lowercase-hex.
func (s *Store) Save(meta Meta, msgs []brain.Message, forest []ToolFrame) error {
	if !validID(meta.ID) {
		return ErrInvalidID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.readLocked(meta.ID)
	if !ok {
		rec = record{Meta: Meta{ID: meta.ID, Created: time.Now()}}
	}
	if meta.Name != "" {
		rec.Name = meta.Name
	}
	if meta.Origin != "" {
		rec.Origin = meta.Origin
	}
	if meta.Agent != "" {
		rec.Agent = meta.Agent
	}
	rec.Updated = time.Now()
	rec.Messages = msgs
	rec.Forest = forest
	rec.Turns = countUserTurns(msgs)
	return s.writeLocked(rec)
}

// Prune enforces a retention cap on one agent's saved runs: it keeps the keepN
// most-recently-updated records whose Meta.Agent == agent and deletes the rest,
// so a frequent cron never floods the picker. Root/user chats (empty Agent) are
// never touched. keepN <= 0 keeps nothing.
func (s *Store) Prune(agent string, keepN int) {
	if agent == "" {
		return // never prune root/user chats
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	var runs []Meta
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		if rec, ok := s.readLocked(chatIDFromFile(e.Name())); ok && rec.Agent == agent {
			runs = append(runs, rec.Meta)
		}
	}
	if len(runs) <= keepN {
		return
	}
	slices.SortFunc(runs, func(a, b Meta) int { return b.Updated.Compare(a.Updated) })
	for _, m := range runs[max(keepN, 0):] {
		_ = os.Remove(s.path(m.ID))
	}
}

// Rename changes a chat's display name.
func (s *Store) Rename(id, name string) error {
	if name == "" {
		return nil // empty rename is a deliberate no-op, not a rejection
	}
	if !validID(id) {
		return ErrInvalidID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.readLocked(id)
	if !ok {
		return nil
	}
	rec.Name = name
	rec.Updated = time.Now()
	return s.writeLocked(rec)
}

// Delete removes a chat. Returns ErrInvalidID for a malformed id; a no-op (nil) for a
// valid-but-unknown id.
func (s *Store) Delete(id string) error {
	if !validID(id) {
		return ErrInvalidID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path(id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *Store) path(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *Store) readLocked(id string) (record, bool) {
	if !validID(id) {
		return record{}, false
	}
	b, err := os.ReadFile(s.path(id))
	if err != nil {
		return record{}, false
	}
	var rec record
	if json.Unmarshal(b, &rec) != nil {
		return record{}, false
	}
	return rec, true
}

func (s *Store) writeLocked(rec record) error {
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(rec.ID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(rec.ID))
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand.Read never fails on supported platforms; a zero id would collide
	}
	return hex.EncodeToString(b)
}

// validID confines a (possibly client-supplied) chat id to a safe filename — lowercase hex
// as minted by newID. Anything else (empty, path separators, "..") is rejected BEFORE it
// reaches the filesystem, so a chat id can never escape the chats directory.
func validID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func chatIDFromFile(name string) string {
	return name[:len(name)-len(filepath.Ext(name))]
}

func countUserTurns(msgs []brain.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == "user" {
			n++
		}
	}
	return n
}
