package agentkit

import (
	"sort"
	"sync"
)

// Store persists a session's transcript. Session is this package's runtime aggregate, so an
// unqualified Store (and Manager) unambiguously means "session" — tools and skills are Sets, not
// stores. The consumer supplies the implementation (file-backed, DB, …); MemStore is the in-memory
// default for tests and simple use.
type Store interface {
	Save(id string, msgs []Message) error
	Load(id string) ([]Message, error)
	List() ([]string, error)
}

// MemStore is an in-memory Store.
type MemStore struct {
	mu       sync.RWMutex
	sessions map[string][]Message
}

// NewMemStore builds an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{sessions: make(map[string][]Message)}
}

func (s *MemStore) Save(id string, msgs []Message) error {
	cp := make([]Message, len(msgs))
	copy(cp, msgs)
	s.mu.Lock()
	s.sessions[id] = cp
	s.mu.Unlock()
	return nil
}

// Load returns the stored transcript, or nil (an empty history — not an error) if id is unknown.
func (s *MemStore) Load(id string) ([]Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	msgs, ok := s.sessions[id]
	if !ok {
		return nil, nil
	}
	cp := make([]Message, len(msgs))
	copy(cp, msgs)
	return cp, nil
}

func (s *MemStore) List() ([]string, error) {
	s.mu.RLock()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	s.mu.RUnlock()
	sort.Strings(ids)
	return ids, nil
}

var _ Store = (*MemStore)(nil)
