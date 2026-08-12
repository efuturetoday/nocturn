package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"math/big"
	"time"
)

// BootstrapMaxTries is how many wrong guesses a bootstrap code survives before it is dropped.
//
// The same rule and the same number as joinMaxTries, for the same reason: six digits is a million
// codes, the window is minutes, and an HTTP handler on a LAN answers thousands of requests a second —
// so without a counter the code is not a secret, it is a formality. Five is chosen for the human, not
// the attacker: someone reading a code off their own console does not need a sixth attempt, and a
// burned code is not a lockout because `nocturn pair` mints another one.
const BootstrapMaxTries = 5

// otp is the one-time bootstrap code that pairs the FIRST device: a short numeric code, single-use,
// expiring, and rate-limited. It never persists (in-memory only), so a restart mints a fresh one and
// an unpaired daemon is never left with a stale code on disk.
type otp struct {
	code     string
	expires  time.Time
	attempts int
}

// ArmBootstrap mints a fresh pairing code valid for ttl and returns it to show the operator,
// replacing any code already pending.
//
// It asks nothing about the registry. WHETHER a household still needs a bootstrap code is the
// question "can anything already here bring a device in by itself?", and that is a question about
// what a class may DO — which this package deliberately does not know. The caller reads Classes,
// decides, and calls this. See serve.serveOn, where the decision now lives.
func (s *Store) ArmBootstrap(ttl time.Duration) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	code := otpCode()
	s.otp = &otp{code: code, expires: time.Now().Add(ttl)}
	return code
}

// BootstrapPending reports whether a pairing code is armed and still within its window.
//
// It is the fact behind "which screen should a fresh client show" — enter a bootstrap code, or ask an
// existing device to relay a join code. Reporting that a code exists reveals nothing a caller could
// not learn by attempting to redeem one, and the code itself never leaves this package.
func (s *Store) BootstrapPending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.otp != nil && time.Now().Before(s.otp.expires)
}

// valid reports whether code matches and has not expired (constant-time compare).
func (o *otp) valid(code string) bool {
	if time.Now().After(o.expires) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(o.code), []byte(code)) == 1
}

// otpCode returns a uniform random 6-digit code.
func otpCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		panic("auth: crypto/rand failed: " + err.Error())
	}
	return fmt.Sprintf("%06d", n.Int64())
}
