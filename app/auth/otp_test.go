package auth_test

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/efuturetoday/nocturn/app/auth"
)

func TestBootstrap_OnlyWhenNoDevicePaired(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)

	code := s.Bootstrap(bootstrapTTL)
	if len(code) != 6 {
		t.Fatalf("fresh Bootstrap code = %q, want 6 digits", code)
	}
	if _, err := s.Pair(code, "first", "ios"); err != nil {
		t.Fatalf("Pair: %v", err)
	}

	// A device now exists: no bootstrap is needed, so none is armed.
	if again := s.Bootstrap(bootstrapTTL); again != "" {
		t.Errorf("Bootstrap after a paired device = %q, want \"\"", again)
	}
}

func TestOTPCode_FormatAndRange(t *testing.T) {
	t.Parallel()
	// Re-arming a fresh store yields a new random code each time; sample many to
	// exercise the zero-padded 6-digit formatting across the value range.
	for i := 0; i < 500; i++ {
		s, _ := newStore(t)
		code := s.Bootstrap(bootstrapTTL)
		if len(code) != 6 {
			t.Fatalf("code %q length = %d, want 6 (must be zero-padded)", code, len(code))
		}
		for _, r := range code {
			if r < '0' || r > '9' {
				t.Fatalf("code %q contains a non-digit", code)
			}
		}
	}
}

func TestOTPValid_Expired(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const ttl = 5 * time.Minute

		// Inside the window the code pairs.
		valid, _ := newStore(t)
		code := valid.Bootstrap(ttl)
		time.Sleep(ttl - time.Nanosecond)
		synctest.Wait()
		if _, err := valid.Pair(code, "in-window", "ios"); err != nil {
			t.Fatalf("Pair just before expiry: %v", err)
		}

		// A separately-armed code that ages past its ttl is rejected.
		expired, _ := newStore(t)
		staleCode := expired.Bootstrap(ttl)
		time.Sleep(ttl + time.Nanosecond)
		synctest.Wait()
		if _, err := expired.Pair(staleCode, "late", "ios"); !errors.Is(err, auth.ErrPairing) {
			t.Fatalf("Pair after expiry: got %v, want ErrPairing", err)
		}
	})
}

// mutateDigit returns a 6-digit string guaranteed to differ from code by one digit.
func mutateDigit(code string) string {
	b := []byte(code)
	if b[0] == '0' {
		b[0] = '1'
	} else {
		b[0] = '0'
	}
	return string(b)
}
