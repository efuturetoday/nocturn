package hitl

import (
	"strings"
	"testing"
)

func TestToken_SignVerifyRoundtrip(t *testing.T) {
	key := []byte("host-key")
	s := sign(key, token{id: "abc", nonce: "n1", expires: 1000, outcome: Approved})
	got, err := verifyToken(key, s, 500)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.id != "abc" || got.nonce != "n1" || got.outcome != Approved {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestToken_WrongKeyRejected(t *testing.T) {
	s := sign([]byte("real-key"), token{id: "a", nonce: "n", expires: 1000, outcome: Approved})
	if _, err := verifyToken([]byte("attacker-key"), s, 500); err == nil {
		t.Fatal("token signed with a different key must be rejected")
	}
}

// An attacker cannot flip Deny into Approve: splicing an Approve payload onto a
// Deny signature (or any tamper) breaks the HMAC.
func TestToken_TamperRejected(t *testing.T) {
	key := []byte("host-key")
	approve := sign(key, token{id: "a", nonce: "n", expires: 1000, outcome: Approved})
	deny := sign(key, token{id: "a", nonce: "n", expires: 1000, outcome: Denied})

	aPayload := strings.SplitN(approve, ".", 2)[0]
	dSig := strings.SplitN(deny, ".", 2)[1]
	forged := aPayload + "." + dSig // Approve payload, Deny's signature

	if _, err := verifyToken(key, forged, 500); err == nil {
		t.Fatal("spliced/tampered token must be rejected")
	}
}

func TestToken_ExpiredRejected(t *testing.T) {
	key := []byte("host-key")
	s := sign(key, token{id: "a", nonce: "n", expires: 1000, outcome: Approved})
	if _, err := verifyToken(key, s, 1000); err == nil {
		t.Fatal("token at exactly its expiry must be rejected")
	}
	if _, err := verifyToken(key, s, 2000); err == nil {
		t.Fatal("expired token must be rejected")
	}
}
