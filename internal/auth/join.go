package auth

import (
	"crypto/subtle"
	"time"
)

const (
	joinTTL      = 10 * time.Minute // how long a pending join stays confirmable
	joinMaxTries = 5                // wrong-code attempts before a join is dropped (brute-force guard)
)

// PendingJoin is a second device's request awaiting confirmation, shown to an ALREADY-PAIRED device
// so a human can relay the code. The code is never returned to the joining device.
type PendingJoin struct {
	JoinID   string `json:"joinId"`
	Name     string `json:"name"`
	Platform string `json:"platform,omitempty"`
	Code     string `json:"code"`
}

// join is a pending second-device request (in-memory, transient — a restart clears it).
type join struct {
	code     string
	name     string
	platform string
	expires  time.Time
	attempts int
}

// Join records a second device's request and returns its joinId — never the code. The code is minted
// server-side and revealed only to an already-paired device via PendingJoins; a human relays it.
func (s *Store) Join(name, platform string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == "" {
		name = "device"
	}
	id := newID()
	s.joins[id] = &join{code: otpCode(), name: name, platform: platform, expires: time.Now().Add(joinTTL)}
	return id
}

// PendingJoins lists the open join requests with their codes, for an already-paired device to relay
// to a human. Expired entries are pruned in passing.
func (s *Store) PendingJoins() []PendingJoin {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	out := []PendingJoin{} // never nil, so the wire carries [] not null
	for id, j := range s.joins {
		if now.After(j.expires) {
			delete(s.joins, id)
			continue
		}
		out = append(out, PendingJoin{JoinID: id, Name: j.name, Platform: j.platform, Code: j.code})
	}
	return out
}

// ConfirmJoin redeems a joinId and its relayed code for a new device's bearer. A wrong code is
// counted; past joinMaxTries the join is dropped so the 6-digit code cannot be brute-forced. An
// unknown, expired, or exhausted join fails with ErrPairing.
func (s *Store) ConfirmJoin(joinID, code string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.joins[joinID]
	if !ok || time.Now().After(j.expires) {
		delete(s.joins, joinID)
		return "", ErrPairing
	}
	if subtle.ConstantTimeCompare([]byte(j.code), []byte(code)) != 1 {
		j.attempts++
		if j.attempts >= joinMaxTries {
			delete(s.joins, joinID)
		}
		return "", ErrPairing
	}
	delete(s.joins, joinID) // single-use
	return s.addDevice(j.name, j.platform)
}
