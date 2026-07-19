// Package device is the daemon's device-authorization control plane: which phones and
// desktops are paired, and the short-lived pending pairings that mint a new one. It is the
// auth root for the companion-app server — every WebSocket connection carries a bearer this
// package minted, and the pairing/join handshakes here are the only way to obtain one.
//
// Two pieces, one file:
//   - Store persists the PAIRED devices to devices.json (0600): a device is a name plus the
//     sha256 of its bearer, so the plaintext bearer never touches disk. Verify hashes a
//     presented bearer and looks it up in constant map time.
//   - Pairings (pairing.go) holds the in-memory, short-lived PENDING pairings — a fresh-boot
//     bootstrap QR/OTP, or a running daemon's join code. Redeeming one mints a device here.
//
// Nothing does I/O beyond the one JSON file; it is stdlib-only and safe for concurrent use.
package device

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Platform is a device's OS — it routes the push provider. Exported so callers use the constant,
// not a magic string. Empty is treated as iOS for legacy devices paired before platform existed.
const (
	PlatformIOS     = "ios"     // Apple Push (APNs)
	PlatformAndroid = "android" // Firebase Cloud Messaging (FCM) — sender not built yet
	PlatformWeb     = "web"     // a browser; no native push token
)

// Device is one paired client: a stable id, a human name, when it was added, and (once the app
// registers it) its APNs push token. The bearer itself is NOT stored — only its sha256 is (the
// Store map key), so a leaked devices.json cannot impersonate a device. A non-empty PushToken
// means the device is reachable out-of-band (the user granted push); empty means it is not.
type Device struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Added     time.Time `json:"added"`
	PushToken string    `json:"pushToken,omitempty"`
	Platform  string    `json:"platform,omitempty"` // "ios" (APNs) | "android" (FCM) — routes the push provider
	LastUsed  time.Time `json:"lastUsed,omitzero"`  // last time this device authenticated a connection (omitzero: Go 1.24+)
}

// touchFlushInterval coalesces LastUsed writes: at most one disk flush per interval no matter how
// often a device reconnects. Between flushes the timestamp lives in memory (and any other persist
// piggybacks it out); Flush on shutdown captures the tail.
const touchFlushInterval = 5 * time.Minute

// Store is the file-backed set of paired devices (devices.json, 0600). It maps the sha256 of
// each device's bearer to the device, so Verify is an O(1) hash lookup and the plaintext
// bearer is never persisted. Safe for concurrent use.
type Store struct {
	path        string
	now         func() time.Time
	OnChange    func()          // fired on a membership/token change (pair/revoke/register), not on Touch
	OnRevoke    func(id string) // fired when a specific device is removed — used to drop its live connection
	mu          sync.Mutex
	byHash      map[string]Device
	lastPersist time.Time // when the file was last written — the Touch coalescing throttle
}

// changed fires the OnChange hook (if set) so the app server can push the fresh device list. It
// is called AFTER the mutating method has released s.mu, so the callback can safely re-enter.
func (s *Store) changed() {
	if s.OnChange != nil {
		s.OnChange()
	}
}

// Load reads path into a Store; a missing or malformed file yields an empty store rather than
// failing daemon boot (fail-safe, mirroring the other control-plane stores). An empty path is
// in-memory only (tests).
func Load(path string) *Store {
	s := &Store{path: path, byHash: map[string]Device{}, now: time.Now}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var m map[string]Device
	if json.Unmarshal(data, &m) == nil && m != nil { // a literal `null` unmarshals to a nil map
		s.byHash = m
	}
	return s
}

// Add mints a fresh 32-byte bearer, stores the device (name + platform) under the bearer's
// sha256, persists, and returns the new id and the RAW bearer. That return is the only time the
// plaintext bearer exists — it cannot be recovered from the store afterwards. platform ("ios" |
// "android" | "") is recorded at pairing so a later push registration only needs the token.
func (s *Store) Add(name, platform string) (id, bearer string, err error) {
	bearer = hexToken(32)
	dev := Device{ID: hexToken(16), Name: name, Added: s.now(), Platform: platform}
	hash := hashHex(bearer)

	s.mu.Lock()
	s.byHash[hash] = dev
	perr := s.persist()
	if perr != nil {
		delete(s.byHash, hash) // keep memory consistent with the (un)written file
	}
	s.mu.Unlock()
	if perr != nil {
		return "", "", perr
	}
	s.changed()
	return dev.ID, bearer, nil
}

// Verify reports the device a bearer belongs to, or false if it is unknown. The lookup key is
// sha256(bearer), so no timing side channel reveals anything about an unknown bearer.
func (s *Store) Verify(bearer string) (Device, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.byHash[hashHex(bearer)]
	return d, ok
}

// Touch records that the device with the given id just authenticated a connection. The timestamp
// updates in memory immediately; it is flushed to disk at most once per touchFlushInterval, so a
// reconnect storm cannot turn a query into a write storm. Call it once per accepted connection.
func (s *Store) Touch(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for h, d := range s.byHash {
		if d.ID == id {
			d.LastUsed = s.now()
			s.byHash[h] = d
			if s.now().Sub(s.lastPersist) >= touchFlushInterval {
				_ = s.persist()
			}
			return
		}
	}
}

// Flush writes any in-memory changes coalesced by Touch to disk. Call it on shutdown so the last
// LastUsed timestamps are not lost.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persist()
}

// Remove drops the device with the given id and persists; it reports whether the id existed.
func (s *Store) Remove(id string) bool {
	s.mu.Lock()
	removed := false
	for h, d := range s.byHash {
		if d.ID == id {
			delete(s.byHash, h)
			_ = s.persist()
			removed = true
			break
		}
	}
	s.mu.Unlock()
	if removed {
		if s.OnRevoke != nil {
			s.OnRevoke(id) // drop the device's live connection before announcing the new list
		}
		s.changed()
	}
	return removed
}

// List returns every paired device, oldest first.
func (s *Store) List() []Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Device, 0, len(s.byHash))
	for _, d := range s.byHash {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Added.Before(out[j].Added) })
	return out
}

// SetPushToken records (or, with an empty token, clears) the push token and platform of the
// device with the given id, and persists. It reports whether the id existed. platform routes the
// provider later (ios→APNs, android→FCM). Clearing is how a user who revoked push in the app
// becomes out-of-band-unreachable again.
func (s *Store) SetPushToken(id, token, platform string) bool {
	s.mu.Lock()
	found := false
	for h, d := range s.byHash {
		if d.ID == id {
			d.PushToken, d.Platform = token, platform
			s.byHash[h] = d
			_ = s.persist()
			found = true
			break
		}
	}
	s.mu.Unlock()
	if found {
		s.changed()
	}
	return found
}

// RemovePushToken clears whichever device carries the given push token (APNs reported it dead:
// 410 / BadDeviceToken), and persists if it changed. The device itself stays paired.
func (s *Store) RemovePushToken(token string) {
	if token == "" {
		return
	}
	s.mu.Lock()
	cleared := false
	for h, d := range s.byHash {
		if d.PushToken == token {
			d.PushToken = ""
			s.byHash[h] = d
			_ = s.persist()
			cleared = true
			break
		}
	}
	s.mu.Unlock()
	if cleared {
		s.changed()
	}
}

// PushTarget is one out-of-band-reachable device: its push token and the platform that decides
// the provider (ios→APNs, android→FCM).
type PushTarget struct {
	Token    string
	Platform string
}

// PushTargets returns every registered (non-empty) push token with its platform — the
// out-of-band-reachable devices, so a caller can route each to the right push provider.
func (s *Store) PushTargets() []PushTarget {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PushTarget, 0, len(s.byHash))
	for _, d := range s.byHash {
		if d.PushToken != "" {
			out = append(out, PushTarget{Token: d.PushToken, Platform: d.Platform})
		}
	}
	return out
}

// CanOOB reports whether any paired device can receive an out-of-band push right now.
func (s *Store) CanOOB() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.byHash {
		if d.PushToken != "" {
			return true
		}
	}
	return false
}

// Reset removes every paired device and persists — the operator's recovery path: after
// `serve --reset-pairing` the store is empty, so the daemon re-opens the bootstrap pairing.
func (s *Store) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byHash = map[string]Device{}
	return s.persist()
}

// persist writes the store atomically (temp + rename), 0600. The caller holds s.mu. An empty
// path is in-memory only.
func (s *Store) persist() error {
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(s.byHash, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.lastPersist = s.now() // reset the Touch coalescing throttle on every successful write
	return nil
}

// hashHex is the storage hash of a bearer: sha256, hex-encoded. It is what both Add and Verify
// key on, so the plaintext bearer is never held.
func hashHex(bearer string) string {
	sum := sha256.Sum256([]byte(bearer))
	return hex.EncodeToString(sum[:])
}

// hexToken returns n cryptographically random bytes, hex-encoded. It backs device ids (16) and
// bearers/QR secrets (32). crypto/rand.Read cannot fail on supported platforms; a silent
// all-zero fill would be predictable and colliding, so make the impossible loud.
func hexToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("device: out of randomness: " + err.Error())
	}
	return hex.EncodeToString(b)
}
