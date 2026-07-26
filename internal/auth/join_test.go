package auth_test

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/efuturetoday/nocturn/internal/auth"
)

// joinTTLTest mirrors the unexported joinTTL (10m) from join.go; the external test package
// cannot reference the constant, so the value is duplicated with intent here.
const joinTTLTest = 10 * time.Minute

func TestJoin_NeverReturnsCode(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)

	id := s.Join("phone", "ios")

	pending := s.PendingJoins()
	if len(pending) != 1 {
		t.Fatalf("PendingJoins = %d, want 1", len(pending))
	}
	// The joining device only ever learns the joinId; the code stays server-side.
	if id == pending[0].Code {
		t.Error("Join returned the pairing code; it must return only the joinId")
	}
	if pending[0].JoinID != id {
		t.Errorf("pending joinID = %q, want %q", pending[0].JoinID, id)
	}
}

func TestPendingJoins_ExposesCodeForPairedRelay(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	id := s.Join("phone", "ios")

	pending := s.PendingJoins()
	if len(pending) != 1 {
		t.Fatalf("PendingJoins = %d, want 1", len(pending))
	}
	got := pending[0]
	if got.JoinID != id {
		t.Errorf("joinID = %q, want %q", got.JoinID, id)
	}
	if got.Name != "phone" || got.Platform != "ios" {
		t.Errorf("name/platform = %q/%q, want phone/ios", got.Name, got.Platform)
	}
	if len(got.Code) != 6 {
		t.Errorf("relayed code %q is not 6 digits", got.Code)
	}
}

func TestPendingJoins_EmptyIsNonNilSlice(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)

	got := s.PendingJoins()
	if got == nil {
		t.Error("PendingJoins returned nil; the wire must carry [] not null")
	}
	if len(got) != 0 {
		t.Errorf("PendingJoins = %d, want 0", len(got))
	}
}

func TestConfirmJoin_RightCodeMintsBearer(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	id := s.Join("phone", "ios")
	code := codeFor(t, s, id)

	bearer, err := s.ConfirmJoin(id, code)
	if err != nil {
		t.Fatalf("ConfirmJoin: %v", err)
	}
	if !verified(s, bearer) {
		t.Error("minted bearer is not accepted by Verify")
	}
	// Single-use: the same joinId+code cannot be redeemed twice.
	if _, err := s.ConfirmJoin(id, code); !errors.Is(err, auth.ErrPairing) {
		t.Fatalf("second ConfirmJoin: got %v, want ErrPairing", err)
	}
}

func TestConfirmJoin_WrongCodeIncrementsAndCaps(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	id := s.Join("phone", "ios")
	code := codeFor(t, s, id)
	wrong := mutateDigit(code)

	// joinMaxTries (5) wrong attempts drop the join entirely.
	for i := 0; i < 5; i++ {
		if _, err := s.ConfirmJoin(id, wrong); !errors.Is(err, auth.ErrPairing) {
			t.Fatalf("wrong attempt %d: got %v, want ErrPairing", i, err)
		}
	}
	// After the cap, even the correct code no longer works (brute-force guard).
	if _, err := s.ConfirmJoin(id, code); !errors.Is(err, auth.ErrPairing) {
		t.Fatalf("correct code after cap: got %v, want ErrPairing", err)
	}
	if got := s.PendingJoins(); len(got) != 0 {
		t.Errorf("dropped join still listed: %d pending", len(got))
	}
}

func TestConfirmJoin_UnknownJoinID(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)

	if _, err := s.ConfirmJoin("deadbeef", "123456"); !errors.Is(err, auth.ErrPairing) {
		t.Fatalf("unknown joinID: got %v, want ErrPairing", err)
	}
}

func TestJoin_ExpiresAfterTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _ := newStore(t)
		id := s.Join("phone", "ios")

		// Just inside the window: still confirmable and listed.
		time.Sleep(joinTTLTest - time.Nanosecond)
		synctest.Wait()
		if got := s.PendingJoins(); len(got) != 1 {
			t.Fatalf("before TTL: PendingJoins = %d, want 1", len(got))
		}

		// Past the TTL: PendingJoins prunes it and ConfirmJoin fails.
		time.Sleep(2 * time.Nanosecond)
		synctest.Wait()
		if got := s.PendingJoins(); len(got) != 0 {
			t.Errorf("after TTL: PendingJoins = %d, want 0 (should be pruned)", len(got))
		}
		if _, err := s.ConfirmJoin(id, "000000"); !errors.Is(err, auth.ErrPairing) {
			t.Fatalf("confirm after TTL: got %v, want ErrPairing", err)
		}
	})
}

// codeFor returns the server-minted code for a pending join.
func codeFor(t *testing.T, s *auth.Store, id string) string {
	t.Helper()
	for _, pj := range s.PendingJoins() {
		if pj.JoinID == id {
			return pj.Code
		}
	}
	t.Fatalf("no pending join for id %q", id)
	return ""
}
