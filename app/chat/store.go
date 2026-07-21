// Package chat persists conversations and multiplexes them — the layer agentkit deliberately leaves
// to the consumer. agentkit.Session already loads and saves its transcript through an agentkit.Store
// (WithStore); this package supplies a file-backed one that also carries chat metadata (name, turns,
// timestamps, source), and a Manager that starts and resumes sessions over it.
package chat

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
)

// ErrInvalidID rejects a chat id that isn't a safe filename — a client mints its own ids, so the
// store never trusts one: only non-empty lowercase hex reaches the filesystem (no separators, "..").
var ErrInvalidID = errors.New("chat: invalid id")

// ValidID reports whether id is a safe chat id (non-empty lowercase hex). The server validates a
// client-supplied id with this before it is ever used as a transcript key / filename.
func ValidID(id string) bool {
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

const (
	ext          = ".json" // chat transcript file extension
	tmpSuffix    = ".tmp"  // suffix for the write-then-rename temp file
	nameLimit    = 40      // max runes of the first message kept as a chat name
	previewLimit = 80      // max runes of the last message kept as a list preview
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
	Preview string    `json:"preview,omitempty"` // last message's first line, for the list (à la Apple Mail)
}

// ToolNode is one captured tool call in a turn's forest — an OBSERVABILITY artifact, kept beside the
// agentkit transcript but NOT part of it (the transcript is re-sent to the LLM; this never is). Parent
// is the enclosing live call id (0 = top level), so it reconstructs the nesting the live event stream
// shows — INCLUDING nested host-bridge calls (code_run→http_read) and sub-agent internals, neither of
// which ever reaches the transcript.
type ToolNode struct {
	ID         uint64 `json:"id"`
	Parent     uint64 `json:"parent"`
	Tool       string `json:"tool"`
	Args       string `json:"args,omitempty"`
	Result     string `json:"result,omitempty"`
	Err        string `json:"err,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

// record is the on-disk shape: metadata, the agentkit transcript, and the per-turn tool forest. Tools
// holds one group per turn, index-aligned to the turns (which are 1:1 with the transcript's user
// messages) — the client zips group k onto the k-th turn's assistant bubble.
type record struct {
	Meta     Meta               `json:"meta"`
	Messages []agentkit.Message `json:"messages"`
	Tools    [][]ToolNode       `json:"tools,omitempty"`
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
// chat activity. Guarded by s.mu: the daemon wires this from serve.Serve while agent-scheduler
// goroutines that call Save (hence fireSaved) are already running, so the write must synchronize
// with the read in fireSaved.
func (s *Store) OnSave(fn func(Meta)) {
	s.mu.Lock()
	s.onSave = fn
	s.mu.Unlock()
}

func (s *Store) fireSaved(m Meta) {
	s.mu.Lock()
	fn := s.onSave
	s.mu.Unlock()
	if fn != nil {
		fn(m)
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
	rec.Meta.Preview = previewFrom(msgs)
	err = s.write(rec)
	meta := rec.Meta
	s.mu.Unlock()
	if err == nil {
		s.fireSaved(meta)
	}
	return err
}

// AppendTools appends one turn's captured tool forest (may be empty, to stay index-aligned with the
// turns). A no-op if the chat has no transcript yet. It shares s.mu with Save so the read-modify-write
// never races: Save preserves rec.Tools, AppendTools preserves rec.Messages. It does NOT bump Meta or
// fire the save callback — the turn's Save already did that; this is a pure observability sidecar.
func (s *Store) AppendTools(id string, nodes []ToolNode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.read(id)
	if err != nil || rec == nil {
		return err
	}
	rec.Tools = append(rec.Tools, nodes)
	return s.write(rec)
}

// LoadTools returns the per-turn tool forest for id (nil if none), for rebuilding the nested forest on
// snapshot.
func (s *Store) LoadTools(id string) ([][]ToolNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.read(id)
	if err != nil || rec == nil {
		return nil, err
	}
	return rec.Tools, nil
}

// MarkRead advances the shared read cursor to the chat's Updated, clearing its unread state (on every
// device, once the activity broadcast reaches them). A no-op if the chat is unknown OR already read up
// to Updated — the latter guard matters: without it, marking an already-read chat still writes and
// broadcasts a chat.activity, which a viewing client can echo back into another markRead (a tight
// loop). Marking-read what is already read changes nothing, so it neither writes nor broadcasts.
func (s *Store) MarkRead(id string) error {
	s.mu.Lock()
	rec, err := s.read(id)
	if err != nil || rec == nil {
		s.mu.Unlock()
		return err
	}
	if rec.Meta.Read.Equal(rec.Meta.Updated) {
		s.mu.Unlock()
		return nil // already read up to the latest — no change, no write, no broadcast
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
	metas := []Meta{} // never nil, so the wire carries [] not null
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
	if !ValidID(id) {
		return ErrInvalidID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.path(id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// read loads a chat record, returning (nil, nil) when the file does not exist. Callers hold s.mu.
func (s *Store) read(id string) (*record, error) {
	if !ValidID(id) {
		return nil, ErrInvalidID
	}
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

// previewFrom is the last user/assistant message's first line, trimmed to previewLimit — the
// "what happened last" hint for the chat list (like Apple Mail), far more telling than a turn count.
func previewFrom(msgs []agentkit.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != agentkit.RoleUser && m.Role != agentkit.RoleAssistant {
			continue
		}
		line := strings.TrimSpace(strings.SplitN(m.Content, "\n", 2)[0])
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > previewLimit {
			line = strings.TrimSpace(string(r[:previewLimit])) + "…"
		}
		return line
	}
	return ""
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
