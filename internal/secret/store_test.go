package secret_test

import (
	"reflect"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/internal/secret"
)

// TestStore_GuestViewExposesOnlyExists is the guard test: the only surface a
// guest may hold (GuestView) has exactly one method, Exists, and it returns a
// bool — never a secret value. If someone adds a value-returning method to the
// interface, this fails.
func TestStore_GuestViewExposesOnlyExists(t *testing.T) {
	iface := reflect.TypeOf((*secret.GuestView)(nil)).Elem()
	if got := iface.NumMethod(); got != 1 {
		t.Fatalf("GuestView has %d methods, want exactly 1 (Exists)", got)
	}
	m := iface.Method(0)
	if m.Name != "Exists" {
		t.Fatalf("GuestView method is %q, want Exists", m.Name)
	}
	byteSlice := reflect.TypeOf([]byte(nil))
	for i := range m.Type.NumOut() {
		if m.Type.Out(i) == byteSlice {
			t.Fatalf("GuestView.%s returns []byte — a guest could read a value", m.Name)
		}
	}
}

// TestStore_ValueUnexported_HostOnly is the API-surface guard: no EXPORTED
// method on *Store returns []byte, so nothing a guest could ever be handed
// yields the raw bytes. reflect over a pointer type reports exported methods
// only, which is exactly the guest-reachable surface.
func TestStore_ValueUnexported_HostOnly(t *testing.T) {
	st := reflect.TypeOf(secret.NewStore())
	byteSlice := reflect.TypeOf([]byte(nil))
	for i := range st.NumMethod() {
		m := st.Method(i)
		for j := range m.Type.NumOut() {
			if m.Type.Out(j) == byteSlice {
				t.Fatalf("exported (*Store).%s returns []byte — leaks a value", m.Name)
			}
		}
	}
}

func TestStore_Exists_PresenceNotValue(t *testing.T) {
	s := secret.NewStore()
	if s.Exists("absent") {
		t.Fatal("Exists reported an unset secret as present")
	}
	s.Set("api", []byte("token-bytes"))
	if !s.Exists("api") {
		t.Fatal("Exists reported a set secret as absent")
	}
	// Exists reveals only presence — its return type is bool, enforced above by
	// the guard tests. Here we only confirm the presence semantics.
}

func TestStore_SetReplaces(t *testing.T) {
	s := secret.NewStore()
	s.Set("api", []byte("first"))
	s.Set("api", []byte("second"))
	if !s.Exists("api") {
		t.Fatal("secret vanished after replace")
	}
	// Value replacement is observable only host-side (via the Vault); presence
	// stays true across a replace, which is all the guest surface reveals.
}

// TestStore_ConcurrentSetExists_NoRace exercises the mutex under -race: many
// goroutines Set and Exists concurrently.
func TestStore_ConcurrentSetExists_NoRace(t *testing.T) {
	s := secret.NewStore()
	const n = 50
	var wg sync.WaitGroup
	wg.Add(2 * n)
	for i := range n {
		go func() {
			defer wg.Done()
			s.Set("k", []byte{byte(i)})
		}()
		go func() {
			defer wg.Done()
			_ = s.Exists("k")
		}()
	}
	wg.Wait()
	if !s.Exists("k") {
		t.Fatal("secret missing after concurrent sets")
	}
}
