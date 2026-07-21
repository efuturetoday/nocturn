// Package auth is the daemon's device registry: it gates every WebSocket connection on a bearer a
// device obtained by pairing. Bearers are high-entropy random tokens, shown to a device once and
// stored only as a hash — a leaked devices.json cannot be replayed. Pairing the FIRST device uses a
// one-time bootstrap code (see otp.go). This is a process-wide security boundary, so it lives in its
// own package, not folded into serve.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"
)

// ErrPairing is returned by Pair when the bootstrap code is wrong, expired, or already spent.
var ErrPairing = errors.New("auth: invalid or expired pairing code")

// Device is one paired device. The bearer itself is never stored — only its hash.
type Device struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Platform   string    `json:"platform,omitempty"`  // ios|android|web, selects the push provider
	BearerHash string    `json:"bearerHash"`          // hex sha256 of the bearer
	PushToken  string    `json:"pushToken,omitempty"` // APNs/FCM token, for out-of-band wake
	Added      time.Time `json:"added"`
	LastUsed   time.Time `json:"lastUsed,omitzero"`
}

// PushTarget is a device reachable out of band: its push token and platform.
type PushTarget struct {
	Token    string
	Platform string
}

// Store verifies bearers and pairs new devices, persisting device records (hashes only, 0600).
type Store struct {
	path string

	mu      sync.Mutex
	devices []Device
	otp     *otp             // the pending bootstrap code, nil once redeemed/absent
	joins   map[string]*join // pending second-device requests (in-memory, transient)
}

// New opens (or creates) a device store at path.
func New(path string) (*Store, error) {
	s := &Store{path: path, joins: map[string]*join{}}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Verify reports whether bearer belongs to a paired device, comparing hashes in constant time.
func (s *Store) Verify(bearer string) bool {
	if bearer == "" {
		return false
	}
	want := hashBearer(bearer)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if subtle.ConstantTimeCompare([]byte(d.BearerHash), []byte(want)) == 1 {
			return true
		}
	}
	return false
}

// Pair redeems the bootstrap code, registering a device named name (on platform, optional) and
// returning its bearer (shown once, never stored). The code is single-use.
func (s *Store) Pair(code, name, platform string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.otp == nil || !s.otp.valid(code) {
		return "", ErrPairing
	}
	s.otp = nil // single-use: spend it whether or not persistence below succeeds
	return s.addDevice(name, platform)
}

// addDevice mints a bearer and registers a new device, persisting the store. Callers hold s.mu.
func (s *Store) addDevice(name, platform string) (string, error) {
	bearer, err := newBearer()
	if err != nil {
		return "", err
	}
	if name == "" {
		name = "device"
	}
	s.devices = append(s.devices, Device{
		ID:         newID(),
		Name:       name,
		Platform:   platform,
		BearerHash: hashBearer(bearer),
		Added:      time.Now(),
	})
	if err := s.save(); err != nil {
		return "", err
	}
	return bearer, nil
}

// RegisterPush records (or, with an empty token, clears) the push token of the device owning bearer,
// so it can be woken out of band. A no-op if the bearer is unknown.
func (s *Store) RegisterPush(bearer, token, platform string) error {
	want := hashBearer(bearer)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.devices {
		if subtle.ConstantTimeCompare([]byte(s.devices[i].BearerHash), []byte(want)) == 1 {
			s.devices[i].PushToken = token
			if platform != "" {
				s.devices[i].Platform = platform
			}
			return s.save()
		}
	}
	return nil
}

// PushTargets returns every device reachable out of band (has a push token).
func (s *Store) PushTargets() []PushTarget {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []PushTarget
	for _, d := range s.devices {
		if d.PushToken != "" {
			out = append(out, PushTarget{Token: d.PushToken, Platform: d.Platform})
		}
	}
	return out
}

// UpdateLastUsed sets a device's LastUsed to now on connect. A no-op if the bearer is unknown
// (already rejected by Verify).
func (s *Store) UpdateLastUsed(bearer string) {
	want := hashBearer(bearer)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.devices {
		if subtle.ConstantTimeCompare([]byte(s.devices[i].BearerHash), []byte(want)) == 1 {
			s.devices[i].LastUsed = time.Now()
			_ = s.save()
			return
		}
	}
}

func hashBearer(bearer string) string {
	sum := sha256.Sum256([]byte(bearer))
	return hex.EncodeToString(sum[:])
}

// newBearer mints a 256-bit random bearer as base64url.
func newBearer() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// newID mints a short device id. A crypto/rand failure is catastrophic, so it panics.
func newID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic("auth: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// load reads the device list, treating a missing file as empty. Caller need not hold mu (called in New).
func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &s.devices)
}

// save writes the device list atomically (write then rename). Callers hold s.mu.
func (s *Store) save() error {
	data, err := json.MarshalIndent(s.devices, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
