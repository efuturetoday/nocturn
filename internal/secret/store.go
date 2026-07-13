// Package secret is the host's secret store and credential injection. Its
// defining property: a guest can learn that a secret exists but can never read
// its value.
//
// The store is kind-agnostic — it holds bytes, so an API key, a password, or an
// OAuth bearer are all just secrets. OAuth's extra lifecycle (authorization
// flow + token refresh) is a credential concern layered on top later, not part
// of the store itself. The store is in-memory for now; an OS-keychain backend
// comes later.
package secret

import "sync"

// Store holds secrets by name. It is host-trusted; never hand a *Store to a
// guest — hand it a GuestView.
type Store struct {
	mu      sync.Mutex
	secrets map[string][]byte
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{secrets: make(map[string][]byte)}
}

// Set stores (or replaces) a secret. Host-side only.
func (s *Store) Set(name string, value []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets[name] = value
}

// Exists reports whether a secret is present. This is the ONLY read a guest is
// allowed — it reveals presence, never the value.
func (s *Store) Exists(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.secrets[name]
	return ok
}

// value is the host-internal read used for credential injection. It is
// unexported on purpose: no exported method returns a secret value, so nothing
// a guest can reach ever yields the bytes.
func (s *Store) value(name string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.secrets[name]
	return v, ok
}

// GuestView is the only store surface a guest may hold. It exposes presence and
// nothing else — there is deliberately no method that returns a secret value,
// so even a fully compromised guest cannot exfiltrate a credential through it.
type GuestView interface {
	Exists(name string) bool
}

// compile-time proof that *Store satisfies the guest surface.
var _ GuestView = (*Store)(nil)
