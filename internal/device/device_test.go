package device_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/internal/device"
)

func TestStore_AddVerify(t *testing.T) {
	s := device.Load("") // in-memory
	id, bearer, err := s.Add("phone", "ios")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if id == "" || bearer == "" {
		t.Fatalf("Add returned empty id/bearer: %q %q", id, bearer)
	}
	d, ok := s.Verify(bearer)
	if !ok {
		t.Fatal("Verify(bearer) = false, want the paired device")
	}
	if d.ID != id || d.Name != "phone" {
		t.Fatalf("Verify device = %+v, want id=%s name=phone", d, id)
	}
	if _, ok := s.Verify("deadbeef"); ok {
		t.Fatal("Verify(unknown) = true, want false")
	}
	if _, ok := s.Verify(""); ok {
		t.Fatal("Verify(empty) = true, want false")
	}
}

func TestStore_Persistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	s := device.Load(path)
	_, bearer, err := s.Add("laptop", "ios")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// A fresh Load off the same file must recognise the bearer — the hash survived to disk,
	// the plaintext bearer did not.
	reloaded := device.Load(path)
	if _, ok := reloaded.Verify(bearer); !ok {
		t.Fatal("reloaded Verify(bearer) = false, want the persisted device")
	}
	if got := len(reloaded.List()); got != 1 {
		t.Fatalf("reloaded List len = %d, want 1", got)
	}
}

// A devices.json containing a literal `null` is valid JSON that unmarshals to a nil map; Load
// must still yield a writable store (not crash the next pairing write).
func TestStore_LoadNullFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	if err := os.WriteFile(path, []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := device.Load(path)
	if _, _, err := s.Add("phone", "ios"); err != nil { // must not panic on a nil map
		t.Fatalf("Add after loading a null file: %v", err)
	}
}

func TestStore_PushToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	s := device.Load(path)
	id, _, _ := s.Add("phone", "ios")

	if s.CanOOB() {
		t.Fatal("CanOOB before any token = true, want false")
	}
	if !s.SetPushToken(id, "apns-tok-1", "ios") {
		t.Fatal("SetPushToken(known id) = false, want true")
	}
	if s.SetPushToken("nope", "x", "ios") {
		t.Fatal("SetPushToken(unknown id) = true, want false")
	}
	if !s.CanOOB() {
		t.Fatal("CanOOB after SetPushToken = false, want true")
	}
	if tgts := s.PushTargets(); len(tgts) != 1 || tgts[0].Token != "apns-tok-1" || tgts[0].Platform != "ios" {
		t.Fatalf("PushTargets = %v, want one ios/apns-tok-1", tgts)
	}

	// A dead token (APNs 410) clears just the push token, the device stays paired.
	s.RemovePushToken("apns-tok-1")
	if s.CanOOB() {
		t.Fatal("CanOOB after RemovePushToken = true, want false")
	}
	if got := len(s.List()); got != 1 {
		t.Fatalf("List after RemovePushToken = %d, want 1 (device stays)", got)
	}

	// Clearing via an empty token is the revoke-push path; it survives a reload.
	s.SetPushToken(id, "apns-tok-2", "ios")
	s.SetPushToken(id, "", "")
	if device.Load(path).CanOOB() {
		t.Fatal("reloaded CanOOB after clear = true, want false")
	}
}

func TestStore_RemoveReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	s := device.Load(path)
	id, bearer, _ := s.Add("a", "ios")
	if _, _, err := s.Add("b", "ios"); err != nil {
		t.Fatalf("Add b: %v", err)
	}

	if !s.Remove(id) {
		t.Fatal("Remove(id) = false, want true")
	}
	if s.Remove(id) {
		t.Fatal("Remove(id) twice = true, want false")
	}
	if _, ok := s.Verify(bearer); ok {
		t.Fatal("Verify after Remove = true, want false")
	}
	if got := len(s.List()); got != 1 {
		t.Fatalf("List len = %d, want 1 (b remains)", got)
	}

	if err := s.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if got := len(s.List()); got != 0 {
		t.Fatalf("List after Reset = %d, want 0", got)
	}
	// Reset persisted: a fresh Load is empty too.
	if got := len(device.Load(path).List()); got != 0 {
		t.Fatalf("reloaded List after Reset = %d, want 0", got)
	}
}
