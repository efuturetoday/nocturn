package auth_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/auth"
)

// bootstrapTTL is a comfortably-long window for tests that do not exercise expiry.
const bootstrapTTL = 5 * time.Minute

// newStore opens a fresh Store backed by a devices.json in a per-test tempdir.
func newStore(t *testing.T) (*auth.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "devices.json")
	s, err := auth.New(path)
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	return s, path
}

// pairFirst pairs the first device via the bootstrap code and returns its bearer.
func pairFirst(t *testing.T, s *auth.Store) string {
	t.Helper()
	code := s.Bootstrap(bootstrapTTL)
	if code == "" {
		t.Fatal("Bootstrap returned empty code on a fresh store")
	}
	bearer, err := s.Pair(code, "first", "ios")
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	return bearer
}

// joinDevice relays a second device through Join → PendingJoins → ConfirmJoin and returns its bearer.
func joinDevice(t *testing.T, s *auth.Store, name, platform string) string {
	t.Helper()
	id := s.Join(name, platform)
	var code string
	for _, pj := range s.PendingJoins() {
		if pj.JoinID == id {
			code = pj.Code
		}
	}
	if code == "" {
		t.Fatalf("no pending code for joinID %q", id)
	}
	bearer, err := s.ConfirmJoin(id, code)
	if err != nil {
		t.Fatalf("ConfirmJoin: %v", err)
	}
	return bearer
}

// mutateOne flips the first byte so the result is guaranteed to differ from the input.
func mutateOne(s string) string {
	b := []byte(s)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	return string(b)
}

// persistedDevice mirrors the on-disk JSON shape so tests can inspect what was written.
type persistedDevice struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Platform   string    `json:"platform"`
	BearerHash string    `json:"bearerHash"`
	PushToken  string    `json:"pushToken"`
	LastUsed   time.Time `json:"lastUsed"`
}

func readDevices(t *testing.T, path string) []persistedDevice {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read devices.json: %v", err)
	}
	var out []persistedDevice
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal devices.json: %v", err)
	}
	return out
}

func TestVerify_UnknownBearerRejected(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)

	if s.Verify("") {
		t.Error("empty bearer accepted; would let an unauthenticated /ws connect")
	}
	if s.Verify("this-token-was-never-issued") {
		t.Error("random bearer accepted on empty store")
	}

	// A paired device must not make an unrelated bearer suddenly valid.
	pairFirst(t, s)
	if s.Verify("still-not-a-real-bearer") {
		t.Error("random bearer accepted after a device was paired")
	}
}

func TestVerify_PairedBearerAccepted(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	bearer := pairFirst(t, s)

	if !s.Verify(bearer) {
		t.Fatal("the exact issued bearer was rejected")
	}
	if mutated := mutateOne(bearer); s.Verify(mutated) {
		t.Errorf("a one-character mutation of the bearer was accepted (%q); compare must be exact", mutated)
	}
}

func TestPair_FirstDeviceConsumesBootstrapCode(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	code := s.Bootstrap(bootstrapTTL)

	if _, err := s.Pair(code, "first", "ios"); err != nil {
		t.Fatalf("first Pair: %v", err)
	}
	// Single-use: the same code cannot pair a second device.
	if _, err := s.Pair(code, "second", "ios"); !errors.Is(err, auth.ErrPairing) {
		t.Fatalf("reusing the bootstrap code: got %v, want ErrPairing", err)
	}
}

func TestPair_WrongCodeRejected(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	code := s.Bootstrap(bootstrapTTL)

	wrong := mutateDigit(code)
	if _, err := s.Pair(wrong, "dev", "ios"); !errors.Is(err, auth.ErrPairing) {
		t.Fatalf("wrong code: got %v, want ErrPairing", err)
	}
	// A wrong attempt must not spend the armed code — the right one still works.
	if _, err := s.Pair(code, "dev", "ios"); err != nil {
		t.Fatalf("correct code after a wrong attempt: %v", err)
	}
}

func TestPair_NoBootstrapArmed(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)

	if _, err := s.Pair("123456", "dev", "ios"); !errors.Is(err, auth.ErrPairing) {
		t.Fatalf("Pair with no armed bootstrap: got %v, want ErrPairing", err)
	}
}

func TestPair_EmptyNameDefaults(t *testing.T) {
	t.Parallel()
	s, path := newStore(t)
	code := s.Bootstrap(bootstrapTTL)
	if _, err := s.Pair(code, "", ""); err != nil {
		t.Fatalf("Pair: %v", err)
	}

	devices := readDevices(t, path)
	if len(devices) != 1 {
		t.Fatalf("device count = %d, want 1", len(devices))
	}
	if devices[0].Name != "device" {
		t.Errorf("empty name persisted as %q, want default %q", devices[0].Name, "device")
	}
}

func TestRegisterPush_UnknownBearerNoOp(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	pairFirst(t, s)

	if err := s.RegisterPush("not-a-real-bearer", "push-tok", "ios"); err != nil {
		t.Fatalf("RegisterPush unknown bearer: %v", err)
	}
	if got := s.PushTargets(); len(got) != 0 {
		t.Errorf("PushTargets = %d, want 0 (unknown bearer must not register)", len(got))
	}
}

func TestRegisterPush_EmptyTokenClears(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	bearer := pairFirst(t, s)

	if err := s.RegisterPush(bearer, "push-tok", "ios"); err != nil {
		t.Fatalf("RegisterPush set: %v", err)
	}
	if got := s.PushTargets(); len(got) != 1 {
		t.Fatalf("after set: PushTargets = %d, want 1", len(got))
	}
	if err := s.RegisterPush(bearer, "", ""); err != nil {
		t.Fatalf("RegisterPush clear: %v", err)
	}
	if got := s.PushTargets(); len(got) != 0 {
		t.Errorf("after clear: PushTargets = %d, want 0", len(got))
	}
}

func TestPushTargets_OnlyTokened(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	pairFirst(t, s) // no push token
	second := joinDevice(t, s, "second", "android")

	if err := s.RegisterPush(second, "tok-2", "android"); err != nil {
		t.Fatalf("RegisterPush: %v", err)
	}

	targets := s.PushTargets()
	if len(targets) != 1 {
		t.Fatalf("PushTargets = %d, want 1 (only the tokened device)", len(targets))
	}
	if targets[0].Token != "tok-2" || targets[0].Platform != "android" {
		t.Errorf("target = %+v, want {Token:tok-2 Platform:android}", targets[0])
	}
}

func TestUpdateLastUsed_SetsTimestamp(t *testing.T) {
	t.Parallel()
	s, path := newStore(t)
	bearer := pairFirst(t, s)

	if lu := readDevices(t, path)[0].LastUsed; !lu.IsZero() {
		t.Fatalf("LastUsed before connect = %v, want zero", lu)
	}
	s.UpdateLastUsed(bearer)
	if lu := readDevices(t, path)[0].LastUsed; lu.IsZero() {
		t.Error("UpdateLastUsed did not set LastUsed")
	}
}

func TestUpdateLastUsed_UnknownNoOp(t *testing.T) {
	t.Parallel()
	s, path := newStore(t)
	pairFirst(t, s)

	s.UpdateLastUsed("not-a-real-bearer") // must not panic or touch any device
	if lu := readDevices(t, path)[0].LastUsed; !lu.IsZero() {
		t.Errorf("unknown bearer changed a device's LastUsed to %v", lu)
	}
}

func TestPersistence_HashesOnlyNeverBearer(t *testing.T) {
	t.Parallel()
	s, path := newStore(t)
	bearer := pairFirst(t, s)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read devices.json: %v", err)
	}
	if strings.Contains(string(raw), bearer) {
		t.Error("the bearer itself was written to disk; a leaked file would be replayable")
	}
	sum := sha256.Sum256([]byte(bearer))
	if want := hex.EncodeToString(sum[:]); !strings.Contains(string(raw), want) {
		t.Errorf("bearerHash %q not found on disk", want)
	}

	// A reopened store must still accept the original bearer (hash round-trips).
	reloaded, err := auth.New(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !reloaded.Verify(bearer) {
		t.Error("reloaded store rejected the original bearer")
	}
}

func TestSave_FilePermissions0600(t *testing.T) {
	t.Parallel()
	s, path := newStore(t)
	pairFirst(t, s)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat devices.json: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("devices.json perms = %04o, want 0600", perm)
	}
	// The write is atomic (write-tmp-then-rename): no .tmp must linger.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("leftover %s.tmp after save (stat err = %v)", path, err)
	}
}

func TestNew_MissingFileIsEmpty(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "does-not-exist.json")

	s, err := auth.New(path)
	if err != nil {
		t.Fatalf("New on missing file: %v", err)
	}
	if s.Verify("anything") {
		t.Error("empty store accepted a bearer")
	}
	if got := s.PushTargets(); len(got) != 0 {
		t.Errorf("PushTargets = %d, want 0", len(got))
	}
}

func TestNew_CorruptJSON(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "devices.json")
	if err := os.WriteFile(path, []byte("{ this is not valid json"), 0o600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}

	if _, err := auth.New(path); err == nil {
		t.Error("New on corrupt JSON returned nil error; must fail loudly, not silently empty")
	}
}
