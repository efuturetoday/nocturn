package remindcap

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"
)

// Reminder is one stored, pending reminder.
type Reminder struct {
	ID      string    `json:"id"`
	FireAt  time.Time `json:"fireAt"`
	Message string    `json:"message"`
	Title   string    `json:"title,omitempty"`
}

// Store persists pending reminders to a control-plane JSON file (0600), OUTSIDE the
// model's mount — so the model can neither see nor file.write it, and the only way to
// add/remove one is the gated remind tool (load-bearing like the grant store, ADR-10).
type Store struct {
	path  string
	mu    sync.Mutex
	items map[string]Reminder
}

// LoadStore reads path (a missing file yields an empty store). A malformed file is
// tolerated as empty rather than failing the whole workspace boot.
func LoadStore(path string) *Store {
	s := &Store{path: path, items: map[string]Reminder{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var list []Reminder
	if json.Unmarshal(data, &list) == nil {
		for _, r := range list {
			s.items[r.ID] = r
		}
	}
	return s
}

// Add stores r and persists.
func (s *Store) Add(r Reminder) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[r.ID] = r
	return s.persist()
}

// Remove deletes id and persists; it reports whether the id existed.
func (s *Store) Remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, existed := s.items[id]
	if existed {
		delete(s.items, id)
		_ = s.persist()
	}
	return existed
}

// List returns all pending reminders, sorted by fire time.
func (s *Store) List() []Reminder {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Reminder, 0, len(s.items))
	for _, r := range s.items {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FireAt.Before(out[j].FireAt) })
	return out
}

// persist writes the store atomically (temp + rename), 0600. Caller holds s.mu. An
// empty path is in-memory only (tests).
func (s *Store) persist() error {
	if s.path == "" {
		return nil
	}
	list := make([]Reminder, 0, len(s.items))
	for _, r := range s.items {
		list = append(list, r)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
