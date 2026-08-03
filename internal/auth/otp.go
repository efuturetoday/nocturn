package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"math/big"
	"time"
)

// otp is the one-time bootstrap code that pairs the FIRST device: a short numeric code, single-use
// and expiring. It never persists (in-memory only), so a restart mints a fresh one and an unpaired
// daemon is never left with a stale code on disk.
type otp struct {
	code    string
	expires time.Time
}

// Bootstrap arms a fresh pairing code IF nothing in the registry can bring a phone in by itself,
// valid for ttl, and returns it to show the operator. It returns "" once such a device exists.
//
// The test is on ClassApp, not on the registry being empty, and the difference is the whole flow:
// the daemon enrols its own command line (ClassTool) at startup, and an appliance may be enrolled on
// someone's behalf. Neither can relay a join code — see serve.capabilitiesOf, where a class becomes
// abilities — so counting them as "a device is paired" retires the bootstrap code while nothing is
// left that could pair the first phone, and the household can never be entered at all.
func (s *Store) Bootstrap(ttl time.Duration) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if d.Class == ClassApp {
			return ""
		}
	}
	code := otpCode()
	s.otp = &otp{code: code, expires: time.Now().Add(ttl)}
	return code
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
