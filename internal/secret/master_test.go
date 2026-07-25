package secret_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/efuturetoday/nocturn/internal/secret"
)

// fast keeps scrypt cheap in tests (2^10 instead of the 2^18 default).
var fast = secret.WithWorkFactor(10)

func TestDeriveMaster_EmptyPassphraseOrSalt_Rejected(t *testing.T) {
	cases := []struct {
		name       string
		passphrase string
		salt       []byte
	}{
		{"empty passphrase", "", []byte("salt")},
		{"nil salt", "pw", nil},
		{"empty salt", "pw", []byte{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := secret.DeriveMaster(tc.passphrase, tc.salt, fast); err == nil {
				t.Fatal("DeriveMaster accepted an empty passphrase/salt")
			}
		})
	}
}

func TestWorkspaceKey_DomainSeparated(t *testing.T) {
	m, err := secret.DeriveMaster("passphrase", []byte("salt-16-bytes-!!"), fast)
	if err != nil {
		t.Fatalf("DeriveMaster: %v", err)
	}
	ka := m.WorkspaceKey("alpha")
	kb := m.WorkspaceKey("beta")
	if len(ka) != 32 || len(kb) != 32 {
		t.Fatalf("workspace keys not 32 bytes: %d / %d", len(ka), len(kb))
	}
	if bytes.Equal(ka, kb) {
		t.Fatal("distinct workspaces derived the same key — not domain-separated")
	}
}

func TestWorkspaceKey_DeterministicAnd32Bytes(t *testing.T) {
	salt := []byte("salt-16-bytes-!!")
	m1, err := secret.DeriveMaster("passphrase", salt, fast)
	if err != nil {
		t.Fatalf("DeriveMaster: %v", err)
	}
	m2, err := secret.DeriveMaster("passphrase", salt, fast)
	if err != nil {
		t.Fatalf("DeriveMaster: %v", err)
	}
	k1 := m1.WorkspaceKey("ws")
	k2 := m2.WorkspaceKey("ws")
	if len(k1) != 32 {
		t.Fatalf("workspace key is %d bytes, want 32", len(k1))
	}
	if !bytes.Equal(k1, k2) {
		t.Fatal("same passphrase+salt+workfactor derived different keys")
	}
}

func TestCheckVerifier_WrongPassphrase_False(t *testing.T) {
	salt := []byte("salt-16-bytes-!!")
	right, err := secret.DeriveMaster("correct-horse", salt, fast)
	if err != nil {
		t.Fatalf("DeriveMaster: %v", err)
	}
	blob := right.Verifier()

	if !right.CheckVerifier(blob) {
		t.Fatal("correct passphrase failed its own verifier")
	}
	wrong, err := secret.DeriveMaster("battery-staple", salt, fast)
	if err != nil {
		t.Fatalf("DeriveMaster: %v", err)
	}
	if wrong.CheckVerifier(blob) {
		t.Fatal("wrong passphrase passed the verifier")
	}
}

func TestMasterSalt_RoundTripsThroughSaltFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.json")

	// Missing file surfaces fs.ErrNotExist so the caller does first-time setup.
	if _, _, _, err := secret.ReadMasterSalt(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing salt file: got %v, want fs.ErrNotExist", err)
	}

	salt := []byte("sixteen-byte-slt")
	m, err := secret.DeriveMaster("pw", salt, fast)
	if err != nil {
		t.Fatalf("DeriveMaster: %v", err)
	}
	verifier := m.Verifier()
	if err := secret.WriteMasterSalt(path, salt, 10, verifier); err != nil {
		t.Fatalf("WriteMasterSalt: %v", err)
	}

	gotSalt, gotLogN, gotVerifier, err := secret.ReadMasterSalt(path)
	if err != nil {
		t.Fatalf("ReadMasterSalt: %v", err)
	}
	if !bytes.Equal(gotSalt, salt) || gotLogN != 10 || !bytes.Equal(gotVerifier, verifier) {
		t.Fatalf("round-trip mismatch: salt=%q logN=%d", gotSalt, gotLogN)
	}
	// A master re-derived from the read-back descriptor still checks out.
	m2, err := secret.DeriveMaster("pw", gotSalt, secret.WithWorkFactor(gotLogN))
	if err != nil {
		t.Fatalf("re-derive: %v", err)
	}
	if !m2.CheckVerifier(gotVerifier) {
		t.Fatal("verifier failed after a salt-file round-trip")
	}
}

func TestReadMasterSalt_InvalidFields_Rejected(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{"empty salt", `{"salt":"","logN":18,"verifier":"AQID"}`},
		{"missing salt", `{"logN":18,"verifier":"AQID"}`},
		{"zero logN", `{"salt":"AQID","logN":0,"verifier":"AQID"}`},
		{"negative logN", `{"salt":"AQID","logN":-1,"verifier":"AQID"}`},
		{"empty verifier", `{"salt":"AQID","logN":18,"verifier":""}`},
		{"not json", `not json at all`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "master.json")
			if err := os.WriteFile(path, []byte(tc.json), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if _, _, _, err := secret.ReadMasterSalt(path); err == nil {
				t.Fatal("ReadMasterSalt accepted an invalid descriptor")
			}
		})
	}
}

// TestWithWorkFactor_UsedOverDefault proves the passed cost is honored by the
// derivation: two different work factors yield different keys, so the option is
// not ignored in favor of the built-in default. (We compare two small factors
// rather than deriving at the 2^18 default, which allocates ~256 MiB — see the
// discrepancy note in the report.)
func TestWithWorkFactor_UsedOverDefault(t *testing.T) {
	salt := []byte("salt-16-bytes-!!")
	m10, err := secret.DeriveMaster("pw", salt, secret.WithWorkFactor(10))
	if err != nil {
		t.Fatalf("DeriveMaster(10): %v", err)
	}
	m12, err := secret.DeriveMaster("pw", salt, secret.WithWorkFactor(12))
	if err != nil {
		t.Fatalf("DeriveMaster(12): %v", err)
	}
	if bytes.Equal(m10.WorkspaceKey("ws"), m12.WorkspaceKey("ws")) {
		t.Fatal("different work factors derived the same key — WithWorkFactor ignored")
	}
}
