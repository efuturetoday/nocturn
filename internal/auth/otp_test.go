package auth_test

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/efuturetoday/nocturn/internal/auth"
)

// ArmBootstrap answers only "mint a code". WHETHER a household still needs one is a question about
// what its devices can do, and that decision lives in internal/serve — so this must arm whatever the
// registry already holds, including a device that could perfectly well pair the next one itself.
// A store that second-guessed its caller here is the bug this shape exists to prevent.
func TestArmBootstrap_AsksNothingAboutTheRegistry(t *testing.T) {
	t.Parallel()

	for _, class := range []auth.Class{auth.ClassApp, auth.ClassWeb, auth.ClassTool, auth.ClassAppliance} {
		t.Run(string(class), func(t *testing.T) {
			t.Parallel()
			s, _ := newStore(t)
			if _, err := s.Mint("existing", class); err != nil {
				t.Fatalf("Mint(%s): %v", class, err)
			}

			code := s.ArmBootstrap(bootstrapTTL)
			if len(code) != 6 {
				t.Fatalf("ArmBootstrap with a %s device present = %q, want a 6-digit code", class, code)
			}
			if _, err := s.Pair(code, "phone", "ios", auth.ClassApp); err != nil {
				t.Fatalf("Pair: %v", err)
			}
		})
	}
}

// Re-arming replaces the pending code rather than adding a second one: two codes valid at once would
// double the guessing surface of a 6-digit secret for no gain.
func TestArmBootstrap_ReplacesThePendingCode(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)

	first := s.ArmBootstrap(bootstrapTTL)
	second := s.ArmBootstrap(bootstrapTTL)
	if first == second {
		t.Fatalf("re-arming returned the same code %q — want a fresh one", first)
	}
	if _, err := s.Pair(first, "stale", "ios", auth.ClassApp); !errors.Is(err, auth.ErrPairing) {
		t.Errorf("Pair with the superseded code: got %v, want ErrPairing", err)
	}
	if _, err := s.Pair(second, "current", "ios", auth.ClassApp); err != nil {
		t.Fatalf("Pair with the current code: %v", err)
	}
}

// Six digits is a million codes and the window is minutes, so without a counter the code is not a
// secret — an HTTP handler on a LAN serves thousands of guesses a second. Same rule, same number as
// the join flow's.
func TestPair_WrongCodeIsRateLimited(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	code := s.ArmBootstrap(bootstrapTTL)
	wrong := mutateDigit(code)

	// The first four are survivable: a human mistyping their own console does not get locked out.
	for i := range 4 {
		if _, err := s.Pair(wrong, "guesser", "ios", auth.ClassApp); !errors.Is(err, auth.ErrPairing) {
			t.Fatalf("attempt %d: got %v, want ErrPairing", i+1, err)
		}
	}
	if !s.BootstrapPending() {
		t.Fatal("the code was dropped before the limit — four typos must not cost the operator the code")
	}

	// The fifth spends it, and the RIGHT code no longer works: the guard is worthless if the attacker's
	// last guess merely fails while the code lives on for the next thousand.
	if _, err := s.Pair(wrong, "guesser", "ios", auth.ClassApp); !errors.Is(err, auth.ErrPairing) {
		t.Fatalf("attempt 5: got %v, want ErrPairing", err)
	}
	if s.BootstrapPending() {
		t.Error("the code survived the attempt limit")
	}
	if _, err := s.Pair(code, "operator", "ios", auth.ClassApp); !errors.Is(err, auth.ErrPairing) {
		t.Errorf("the correct code still worked after the limit: %v", err)
	}
}

// Burning a code is not a lockout. Arming again is always available (`nocturn pair`), and the fresh
// code starts with a fresh budget — otherwise the first burn would be permanent.
func TestPair_ReArmingClearsTheAttemptCount(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	first := s.ArmBootstrap(bootstrapTTL)
	for range 5 {
		_, _ = s.Pair(mutateDigit(first), "guesser", "ios", auth.ClassApp)
	}

	second := s.ArmBootstrap(bootstrapTTL)
	for i := range 4 {
		if _, err := s.Pair(mutateDigit(second), "guesser", "ios", auth.ClassApp); !errors.Is(err, auth.ErrPairing) {
			t.Fatalf("attempt %d on the fresh code: got %v", i+1, err)
		}
	}
	if _, err := s.Pair(second, "operator", "ios", auth.ClassApp); err != nil {
		t.Fatalf("the fresh code did not carry its own budget: %v", err)
	}
}

func TestOTPCode_FormatAndRange(t *testing.T) {
	t.Parallel()
	// Re-arming a fresh store yields a new random code each time; sample many to
	// exercise the zero-padded 6-digit formatting across the value range.
	for i := 0; i < 500; i++ {
		s, _ := newStore(t)
		code := s.ArmBootstrap(bootstrapTTL)
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
		code := valid.ArmBootstrap(ttl)
		time.Sleep(ttl - time.Nanosecond)
		synctest.Wait()
		if _, err := valid.Pair(code, "in-window", "ios", auth.ClassApp); err != nil {
			t.Fatalf("Pair just before expiry: %v", err)
		}

		// A separately-armed code that ages past its ttl is rejected.
		expired, _ := newStore(t)
		staleCode := expired.ArmBootstrap(ttl)
		time.Sleep(ttl + time.Nanosecond)
		synctest.Wait()
		if _, err := expired.Pair(staleCode, "late", "ios", auth.ClassApp); !errors.Is(err, auth.ErrPairing) {
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
