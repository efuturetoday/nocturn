package hitl

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

// token is the capability carried by the out-of-band channel: tapping Approve
// or Deny on the phone sends back a token that names the request, a single-use
// nonce, an expiry, and the outcome — all covered by an HMAC signature. The
// outcome is inside the signed payload, so an attacker cannot flip Deny into
// Approve without the host's key. This is the strong channel auth the naive
// "topic name is the only secret" push tools lack.
type token struct {
	id      string
	nonce   string
	expires int64 // unix seconds
	outcome Outcome
}

func (t token) payload() string {
	return strings.Join([]string{
		t.id, t.nonce, strconv.FormatInt(t.expires, 10), strconv.Itoa(int(t.outcome)),
	}, "|")
}

// sign encodes and HMAC-signs a token into a "payload.signature" string.
func sign(key []byte, t token) string {
	p := []byte(t.payload())
	mac := hmac.New(sha256.New, key)
	mac.Write(p)
	return base64.RawURLEncoding.EncodeToString(p) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifyToken checks the signature and expiry and returns the token. A bad
// signature, tampered payload, or expired token is rejected — the outcome can
// be trusted only because the signature covers it.
func verifyToken(key []byte, s string, nowUnix int64) (token, error) {
	parts := strings.SplitN(s, ".", 2)
	if len(parts) != 2 {
		return token{}, errors.New("hitl: malformed token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return token{}, errors.New("hitl: bad token payload")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return token{}, errors.New("hitl: bad token signature")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return token{}, errors.New("hitl: signature mismatch")
	}

	fields := strings.Split(string(payload), "|")
	if len(fields) != 4 {
		return token{}, errors.New("hitl: bad payload")
	}
	expires, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return token{}, errors.New("hitl: bad expiry")
	}
	if nowUnix >= expires {
		return token{}, errors.New("hitl: token expired")
	}
	oc, err := strconv.Atoi(fields[3])
	if err != nil {
		return token{}, errors.New("hitl: bad outcome")
	}
	return token{id: fields[0], nonce: fields[1], expires: expires, outcome: Outcome(oc)}, nil
}
