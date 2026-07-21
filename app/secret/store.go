// Package secret is the host's secret store and credential injection. Its
// defining property: a guest can learn that a secret exists but can never read
// its value.
//
// The store is kind-agnostic — it holds bytes, so an API key, a password, or an
// OAuth bearer (a serialized token, refresh token included) are all just
// secrets. OAuth's extra lifecycle (authorization flow + token refresh) is a
// credential concern layered on top, not part of the store itself. The store is
// the in-memory surface; Vault (vault.go) is its encrypted persistence.
package secret

import "maps"

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

// snapshot returns a copy of the full name→value map. Host-internal like
// value — never exposed to a guest; used only by the Vault to serialize the
// store for encrypted persistence.
func (s *Store) snapshot() map[string][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]byte, len(s.secrets))
	maps.Copy(out, s.secrets)
	return out
}

// knownValues returns a snapshot of every stored secret value. Host-internal
// like value — never exposed to a guest; used only by the leak scanner to catch
// a stored secret being exfiltrated.
func (s *Store) knownValues() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, 0, len(s.secrets))
	for _, v := range s.secrets {
		out = append(out, v)
	}
	return out
}

// GuestView is the only store surface a guest may hold. It exposes presence and
// nothing else — there is deliberately no method that returns a secret value,
// so even a fully compromised guest cannot exfiltrate a credential through it.
type GuestView interface {
	Exists(name string) bool
}

// compile-time proof that *Store satisfies the guest surface.
var _ GuestView = (*Store)(nil)
