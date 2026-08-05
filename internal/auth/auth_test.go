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
// verified keeps the tests reading as a yes/no question where that is all they ask. Lookup replaced
// Verify because callers now need the device's class, not merely whether it exists.
func verified(s *auth.Store, bearer string) bool {
	_, ok := s.Lookup(bearer)
	return ok
}

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

	if verified(s, "") {
		t.Error("empty bearer accepted; would let an unauthenticated /ws connect")
	}
	if verified(s, "this-token-was-never-issued") {
		t.Error("random bearer accepted on empty store")
	}

	// A paired device must not make an unrelated bearer suddenly valid.
	pairFirst(t, s)
	if verified(s, "still-not-a-real-bearer") {
		t.Error("random bearer accepted after a device was paired")
	}
}

func TestVerify_PairedBearerAccepted(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	bearer := pairFirst(t, s)

	if !verified(s, bearer) {
		t.Fatal("the exact issued bearer was rejected")
	}
	if mutated := mutateOne(bearer); verified(s, mutated) {
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
	if !verified(reloaded, bearer) {
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
	if verified(s, "anything") {
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

// The pairing flows both require a screen — one to read a bootstrap code, one to relay a join code —
// so whatever completes them is an app, and must come back able to approve.
func TestPairAndJoin_ProduceApps(t *testing.T) {
	s, _ := newStore(t)
	bearer := pairFirst(t, s)
	if dev, _ := s.Lookup(bearer); dev.Class != auth.ClassApp {
		t.Errorf("paired device class = %q, want app", dev.Class)
	}
	joined := joinDevice(t, s, "phone2", "ios")
	if dev, _ := s.Lookup(joined); dev.Class != auth.ClassApp {
		t.Errorf("joined device class = %q, want app", dev.Class)
	}
}

// Mint is the enrolment path for a device that cannot take part in a code exchange.
func TestMint_EnrolsWithoutACode(t *testing.T) {
	s, _ := newStore(t)
	bearer, err := s.Mint("hallway", auth.ClassAppliance)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	dev, ok := s.Lookup(bearer)
	if !ok {
		t.Fatal("minted bearer does not verify")
	}
	if dev.Class != auth.ClassAppliance || dev.Name != "hallway" {
		t.Errorf("device = %+v, want a satellite named hallway", dev)
	}
}

// A device that could not be persisted was never enrolled. Keeping it in memory would leave a record
// whose bearer nobody holds — unusable for authenticating, yet still enough to look like a populated
// household and retire the bootstrap code.
func TestAddDevice_RollsBackWhenPersistenceFails(t *testing.T) {
	dir := t.TempDir()
	s, err := auth.New(filepath.Join(dir, "devices.json"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Take the write away after the store is open, so the failure lands in save, not in New.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	if _, err := s.Mint("cli", auth.ClassTool); err == nil {
		t.Fatal("Mint into an unwritable directory succeeded, want an error")
	}
	if code := s.Bootstrap(time.Minute); len(code) != 6 {
		t.Errorf("Bootstrap after a failed enrolment = %q, want a 6-digit code — the failed device still counts", code)
	}
}

// A record written before Class existed must not read back as ClassUnknown: it would silently lose
// the right to answer an approval, and the phone would simply never ring again.
func TestLoad_StampsPreClassRecordsAsApps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "devices.json")
	legacy := `[{"id":"a1","name":"iphone","bearerHash":"` + sha256Hex("secret") + `","added":"2025-01-01T00:00:00Z"}]`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := auth.New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	dev, ok := s.Lookup("secret")
	if !ok {
		t.Fatal("legacy device does not verify")
	}
	if dev.Class != auth.ClassApp {
		t.Errorf("legacy class = %q, want app", dev.Class)
	}
	// And the stamp is persisted, so the migration happens once rather than on every start.
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), `"class": "app"`) {
		t.Error("stamp was not written back to disk")
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
