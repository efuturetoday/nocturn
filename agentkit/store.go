package agentkit

import "sync"

// Store persists a session's transcript. Session is this package's runtime aggregate, so an
// unqualified Store (and Manager) unambiguously means "session" — tools and skills are Sets,
// not stores. The consumer supplies the implementation (file-backed, DB, …); MemStore is the
// in-memory default for tests and simple use.
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
func NewMemStore() *MemStore { panic("TODO") }

func (s *MemStore) Save(id string, msgs []Message) error { panic("TODO") }
func (s *MemStore) Load(id string) ([]Message, error)    { panic("TODO") }
func (s *MemStore) List() ([]string, error)              { panic("TODO") }

var _ Store = (*MemStore)(nil)
