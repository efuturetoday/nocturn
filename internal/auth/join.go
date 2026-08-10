package auth

import (
	"crypto/subtle"
	"slices"
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

// CapJoins prunes expired joins and then keeps at most n of the rest, dropping the oldest first.
//
// /join cannot be authenticated — the caller has nothing to authenticate with yet — so without a
// ceiling anyone who can reach the daemon can mint pending joins without limit and bury the
// household's pairing screen. Oldest-first is what keeps the flow usable for the person actually
// standing there: the request they just made is the one that survives the eviction.
func (s *Store) CapJoins(n int) {
	if n < 1 {
		// A ceiling of zero would mean "no device may ever join", which no caller can want, and the
		// arithmetic below would slice past the end reaching for it. Refusing to act beats panicking
		// inside the registry on the way to a pairing screen.
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	ids := make([]string, 0, len(s.joins))
	for id, j := range s.joins {
		if now.After(j.expires) {
			delete(s.joins, id)
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) < n {
		return
	}
	// Sort by expiry, which orders by creation: every join gets the same TTL, so the one expiring
	// soonest is the one made longest ago.
	slices.SortFunc(ids, func(a, b string) int { return s.joins[a].expires.Compare(s.joins[b].expires) })
	for _, id := range ids[:len(ids)-n+1] {
		delete(s.joins, id)
	}
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

// JoinPlatform returns the platform recorded when joinID asked to join, or "" if there is no such
// open join. It reports what was stored and interprets none of it — the caller turns a platform into
// a class, because only the caller knows what a class means.
func (s *Store) JoinPlatform(joinID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.joins[joinID]
	if !ok {
		return ""
	}
	return j.platform
}

// ConfirmJoin redeems a joinId and its relayed code for a new device's bearer, enrolling it as
// class. A wrong code is counted; past joinMaxTries the join is dropped so the 6-digit code cannot
// be brute-forced. An unknown, expired, or exhausted join fails with ErrPairing.
//
// As in Pair, the class is supplied rather than decided: this package writes down what a class is,
// and never what it means.
func (s *Store) ConfirmJoin(joinID, code string, class Class) (string, error) {
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
	return s.addDevice(j.name, j.platform, class)
}
