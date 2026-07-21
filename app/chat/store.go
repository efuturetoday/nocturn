// Package chat persists conversations and multiplexes them — the layer agentkit deliberately leaves
// to the consumer. agentkit.Session already loads and saves its transcript through an agentkit.Store
// (WithStore); this package supplies a file-backed one that also carries chat metadata (name, turns,
// timestamps, source), and a Manager that starts and resumes sessions over it.
package chat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
)

const (
	ext       = ".json" // chat transcript file extension
	tmpSuffix = ".tmp"  // suffix for the write-then-rename temp file
	nameLimit = 40      // max runes of the first message kept as a chat name
)

// Source is who a store's chats belong to. It is a property of the store instance (set once with
// WithSource), stamped into each chat's Meta — so a merged listing across stores is self-describing
// without any per-chat plumbing.
type Source string

const (
	SourceUser  Source = "user"
	SourceAgent Source = "agent"
)

// Meta is a chat's metadata, kept alongside its transcript. The Name is derived from the first user
// message (chats are message-first: born from what you say, never created empty).
type Meta struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Source  Source    `json:"source"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	Read    time.Time `json:"read,omitzero"` // shared read cursor; unread when Updated is later
	Turns   int       `json:"turns"`
}

// record is the on-disk shape: metadata plus the agentkit transcript.
type record struct {
	Meta     Meta               `json:"meta"`
	Messages []agentkit.Message `json:"messages"`
}

// Store is a file-backed agentkit.Store: one JSON file per chat under dir. A chat's file appears on
// its first save, never before. Every chat it saves is stamped with the store's Source.
type Store struct {
	dir    string
	source Source
	mu     sync.Mutex
	onSave func(Meta) // fired (outside the lock) after every persist, for pushing chat activity
}

// OnSave registers a callback run after every persist (a turn save, a markRead), for broadcasting
// chat activity. Set once at wiring time.
func (s *Store) OnSave(fn func(Meta)) { s.onSave = fn }

func (s *Store) fireSaved(m Meta) {
	if s.onSave != nil {
		s.onSave(m)
	}
}

// Option configures a Store.
type Option func(*Store)

// WithSource sets the source stamped into every chat this store saves (default SourceUser). A
// workspace opens one store per source — user chats and agent runs live in separate dirs.
func WithSource(s Source) Option { return func(st *Store) { st.source = s } }

// NewStore opens (creating if needed) a chat store rooted at dir.
func NewStore(dir string, opts ...Option) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, source: SourceUser}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

func (s *Store) path(id string) string { return filepath.Join(s.dir, id+ext) }

// Save persists the transcript for id. On the first save (new record) it stamps Created, the store's
// Source, and the Name derived from the first user message; every save bumps Updated and Turns.
// agentkit calls this once per turn, so it is the per-turn metadata hook.
func (s *Store) Save(id string, msgs []agentkit.Message) error {
	s.mu.Lock()
	rec, err := s.read(id)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	now := time.Now()
	if rec == nil {
		rec = &record{Meta: Meta{ID: id, Name: nameFrom(msgs), Source: s.source, Created: now}}
	}
	rec.Messages = msgs
	rec.Meta.Updated = now
	rec.Meta.Turns++
	err = s.write(rec)
	meta := rec.Meta
	s.mu.Unlock()
	if err == nil {
		s.fireSaved(meta)
	}
	return err
}

// MarkRead advances the shared read cursor to the chat's Updated, clearing its unread state (on every
// device, once the activity broadcast reaches them). A no-op if the chat is unknown.
func (s *Store) MarkRead(id string) error {
	s.mu.Lock()
	rec, err := s.read(id)
	if err != nil || rec == nil {
		s.mu.Unlock()
		return err
	}
	rec.Meta.Read = rec.Meta.Updated
	err = s.write(rec)
	meta := rec.Meta
	s.mu.Unlock()
	if err == nil {
		s.fireSaved(meta)
	}
	return err
}

// Load returns the stored transcript for id, or nil (empty, not an error) if the chat has no file
// yet.
func (s *Store) Load(id string) ([]agentkit.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.read(id)
	if err != nil || rec == nil {
		return nil, err
	}
	return rec.Messages, nil
}

// List returns every stored chat id.
func (s *Store) List() ([]string, error) {
	metas, err := s.Metas()
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(metas))
	for i, m := range metas {
		ids[i] = m.ID
	}
	return ids, nil
}

// Metas returns every chat's metadata, most-recently-updated first.
func (s *Store) Metas() ([]Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var metas []Meta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
			continue
		}
		rec, err := s.read(strings.TrimSuffix(e.Name(), ext))
		if err != nil || rec == nil {
			continue
		}
		metas = append(metas, rec.Meta)
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Updated.After(metas[j].Updated) })
	return metas, nil
}

// Rename sets a chat's display name.
func (s *Store) Rename(id, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.read(id)
	if err != nil || rec == nil {
		return err
	}
	rec.Meta.Name = name
	return s.write(rec)
}

// Delete removes a chat's file. A missing chat is not an error.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.path(id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// read loads a chat record, returning (nil, nil) when the file does not exist. Callers hold s.mu.
func (s *Store) read(id string) (*record, error) {
	data, err := os.ReadFile(s.path(id))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec record
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// write persists a chat record (write then rename). Callers hold s.mu.
func (s *Store) write(rec *record) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(rec.Meta.ID) + tmpSuffix
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(rec.Meta.ID))
}

// nameFrom derives a chat name from its first user message: the first line, trimmed to nameLimit.
func nameFrom(msgs []agentkit.Message) string {
	for _, m := range msgs {
		if m.Role != agentkit.RoleUser {
			continue
		}
		line := strings.TrimSpace(strings.SplitN(m.Content, "\n", 2)[0])
		if r := []rune(line); len(r) > nameLimit {
			line = strings.TrimSpace(string(r[:nameLimit])) + "…"
		}
		if line != "" {
			return line
		}
	}
	return "chat"
}

var _ agentkit.Store = (*Store)(nil)
