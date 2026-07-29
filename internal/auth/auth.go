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

// Class is what a device IS. It is a fact about the device, not a permission: this package stores
// it and never interprets it, so what a class may DO is decided in one place by whoever assembles
// connections (see internal/serve). Storing the category rather than the permissions is deliberate —
// permissions written into devices.json would freeze today's rules into every record, and widening
// them later would need another migration of the kind stampClasses already had to do once.
//
// It is a separate axis from Platform: that one selects the push provider, and a browser and a
// speaker in the hallway could both be "web". There is deliberately no ordering — a class is a
// category, not a level.
//
// The name avoids Kind and Role, both of which already mean something else here: Kind is the gate's
// permission dimension (and, separately, which chat store a command addresses, and which sort of
// notification fired), and agentkit.Role is a message author.
type Class string

const (
	// ClassUnknown is the zero value, and carries the least authority. A record that failed to say
	// what it is must never be assumed trustworthy — the same reasoning as gate.RecallNever and
	// agent.Strict.
	ClassUnknown Class = ""
	// ClassApp is a paired companion app: a screen, a lock, and a person holding it. Someone at it
	// is identified, so what it reports can be taken as that person's answer.
	ClassApp Class = "app"
	// ClassAppliance is a device with NO authenticated input path — a speaker in a hallway, a
	// doorbell, a car. Whoever is present can operate it, and none of them identified themselves, so
	// nothing it reports is anybody's consent.
	//
	// Named for that property rather than for a product: a voice satellite is one of these, but so
	// is anything else whose input nobody signed.
	ClassAppliance Class = "appliance"
	// ClassTool is a local operator tool — the `nocturn` command talking to its own daemon. It is
	// not a device anyone carries and nobody is identified at it, so it consents to nothing; what it
	// may do it may do because whoever runs it already holds the workspace on disk.
	ClassTool Class = "tool"
)

// Device is one paired device. The bearer itself is never stored — only its hash.
type Device struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Class      Class     `json:"class,omitempty"`     // what it is; decides whether it may approve
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

// Lookup returns the device owning bearer, comparing hashes in constant time.
//
// It replaces a plain yes/no check because the answer alone is no longer enough: what a connection
// is allowed to do depends on the device's Class, and a caller that has to ask a second question
// would have to search again.
func (s *Store) Lookup(bearer string) (Device, bool) {
	if bearer == "" {
		return Device{}, false
	}
	want := hashBearer(bearer)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if subtle.ConstantTimeCompare([]byte(d.BearerHash), []byte(want)) == 1 {
			return d, true
		}
	}
	return Device{}, false
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
	return s.addDevice(name, platform, ClassApp)
}

// Mint enrols a device of the given class outright, without a code exchange, and returns its bearer
// (shown once, never stored).
//
// It exists for devices that cannot take part in one. A satellite has no screen to show a code and
// no keyboard to enter one, so the join flow — where a device asks and an already-paired device
// confirms — has nothing to work with. Enrolling itself is not the alternative: a device that can do
// that is the hole. Instead an already-authenticated caller asks for this on its behalf, so the
// human authorises on something that can authorise, and the satellite only ever receives.
//
// The CALLER is responsible for being trusted. This function checks nothing.
func (s *Store) Mint(name string, class Class) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addDevice(name, "", class)
}

// addDevice mints a bearer and registers a new device, persisting the store. Callers hold s.mu.
func (s *Store) addDevice(name, platform string, class Class) (string, error) {
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
		Class:      class,
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
	if err := json.Unmarshal(data, &s.devices); err != nil {
		return err
	}
	return s.stampClasses()
}

// stampClasses gives a class to records written before the field existed.
//
// Without this every already-paired device would read back as ClassUnknown and quietly lose the
// right to answer an approval — the phone would simply never ring again, with nothing in the logs
// to say why. Such a record can only have come from the app pairing flow, because at the time it was
// written nothing else could pair. Fail-closed is right for what is written from now on, and wrong
// as a reading of the past.
func (s *Store) stampClasses() error {
	stamped := false
	for i := range s.devices {
		if s.devices[i].Class == "" {
			s.devices[i].Class = ClassApp
			stamped = true
		}
	}
	if !stamped {
		return nil
	}
	return s.save()
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
