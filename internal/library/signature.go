package library

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Signing is what separates the two things this catalog carries.
//
// A skill is text with zero authority (ADR-10), and TLS to one host is a proportionate control for
// text. A plugin is CODE: the sandbox contains what that code can do — no ambient authority, brokered
// imports, a memory cap and a deadline — but the manifest beside it still ASKS for authority (a cage,
// a credential bound to a host, an OAuth account). Whoever serves the catalog serves the digests too,
// so the digest cannot say who wrote that manifest. A signature can, and only if the key does not
// travel with the document — hence a key pinned in the binary.
//
// The line this draws, stated plainly: a compromised catalog host can offer text nobody vouched for;
// it cannot offer code. That is worth the key management, and it is why plugin entries are refused
// unsigned rather than merely marked.
//
// # What it does NOT cover, on purpose
//
// The LISTING is unsigned: title, description, tags and homepage are the catalog's to write, so a
// host that has been taken over can rebrand a signed plugin ("calendar sync, no mail access") while
// the artifacts underneath stay the ones we signed. What a person tapping install sees of the actual
// grant — tools, cage, credential hosts, scopes — is rendered from the SIGNED manifest, which is the
// half that decides anything. Folding the listing into the statement is a small change and worth
// making the day a second publisher exists.
//
// There is also no freshness: nothing signed is monotonic, so a host can serve yesterday's signed
// catalog forever. Rollback matters once a plugin has been withdrawn for a reason, which is the point
// at which a signed date belongs in the statement.

// signingKeys are the Ed25519 public keys a plugin entry may be signed with, base64 (std, padded).
//
// A LIST rather than one key, because rotation must not need a new binary in every household on the
// same day: a new key is added, entries are re-signed, the old one is dropped a release later.
// Compiled in on purpose — a key read from beside the catalog would be the catalog vouching for
// itself.
var signingKeys = []string{
	// The project's catalog-signing key. Its private half lives with whoever publishes the catalog and
	// never in this repository — `go run catalog/sign.go -keygen` mints a replacement, and rotating
	// means adding the new public key here, re-signing, and dropping the old one a release later.
	"F3C+ynioyuniGrNrGLDl2WEiRGIeIs5CdsU0bMKqOhw=",
}

// devKeyEnv names an ADDITIONAL public key, for developing a plugin against a local catalog without a
// project key. It is opt-in per process and named as what it is: anyone who can set the environment
// of the daemon can already replace its binary.
const devKeyEnv = "NOCTURN_CATALOG_DEV_KEY"

// SignedStatement is the exact byte string a plugin signature covers.
//
// Identity and every digest together, because signing the artifacts separately would let somebody
// keep a signed script and put a different manifest in front of it — the manifest being the half that
// asks for the credential — or swap the bundled skill, which is text that lands in the system prompt.
// A plugin with no skill signs skillSHA as "", so "no skill" is itself signed rather than a gap
// something could be dropped into. The form is newline-separated and field-labelled, so no two
// distinct entries can ever produce the same bytes.
func SignedStatement(id, folder, manifestSHA, scriptSHA, skillSHA string) []byte {
	return []byte("nocturn-plugin-v1\n" +
		"id=" + id + "\n" +
		"folder=" + folder + "\n" +
		"manifest=" + strings.ToLower(manifestSHA) + "\n" +
		"script=" + strings.ToLower(scriptSHA) + "\n" +
		"skill=" + strings.ToLower(skillSHA) + "\n")
}

// signaturePolicy says whether a plugin entry must be signed, which depends on WHERE the catalog came
// from rather than on what it contains.
//
// A signature substitutes for a channel nobody controls. A catalog fetched from a remote host is
// exactly that, and there the substitute is the whole story. A catalog read off this machine — a file
// path, or a server on loopback — has no channel to substitute for: the bytes are already on the host,
// and whoever can write them can drop a folder into plugins/ directly, which has never needed a
// signature. Demanding one there would mean minting keys to install your own plugin from your own
// file, which is the kind of rule people route around rather than follow.
type signaturePolicy bool

const (
	signaturesRequired signaturePolicy = true
	signaturesOptional signaturePolicy = false
)

// verifySignature reports whether the entry carries a signature by a key this build trusts. A
// present-but-invalid signature is refused under either policy: only "absent" is excused locally,
// because a wrong one means somebody tried and something is off.
func verifySignature(it PluginItem, signing signaturePolicy) error {
	if it.Signature == "" && signing == signaturesOptional {
		return nil
	}
	if it.Signature == "" {
		return errors.New("unsigned (a plugin must be signed; a skill need not be)")
	}
	sig, err := base64.StdEncoding.DecodeString(it.Signature)
	if err != nil {
		return fmt.Errorf("signature is not base64: %w", err)
	}
	// The digests are checked against the bytes elsewhere; here they are what was signed, so a
	// malformed one must not be silently treated as an empty string.
	if _, err := hex.DecodeString(it.ManifestSHA); err != nil {
		return fmt.Errorf("manifest_sha256 is not hex: %w", err)
	}
	if _, err := hex.DecodeString(it.ScriptSHA); err != nil {
		return fmt.Errorf("script_sha256 is not hex: %w", err)
	}
	if _, err := hex.DecodeString(it.SkillSHA); err != nil {
		return fmt.Errorf("skill_sha256 is not hex: %w", err)
	}
	msg := SignedStatement(it.ID, it.Folder, it.ManifestSHA, it.ScriptSHA, it.SkillSHA)

	keys, err := trustedKeys()
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return errors.New("this build trusts no catalog signing key, so no plugin can be installed")
	}
	for _, key := range keys {
		if ed25519.Verify(key, msg, sig) {
			return nil
		}
	}
	return errors.New("no trusted key signed this entry")
}

// trustedKeys decodes the compiled-in keys plus the optional development key. A malformed key is a
// mistake somebody made and is reported rather than skipped: silently trusting one fewer key would
// turn a typo into "nothing installs" with no reason given, which is the least debuggable failure
// this package can produce.
//
// The two sources are decoded in one loop but NAMED apart, because the two mistakes want different
// answers: a bad compiled-in key is a bug in this build, a bad NOCTURN_CATALOG_DEV_KEY is a typo in
// somebody's shell.
func trustedKeys() ([]ed25519.PublicKey, error) {
	// A fresh slice rather than append(signingKeys, …): appending to a package-level slice writes
	// into its backing array the day it has spare capacity, and a signing-key list is the last place
	// to leave that lying around.
	sources := make([]string, 0, len(signingKeys)+1)
	sources = append(sources, signingKeys...)
	sources = append(sources, os.Getenv(devKeyEnv))

	out := make([]ed25519.PublicKey, 0, len(sources))
	for i, encoded := range sources {
		if encoded == "" {
			continue
		}
		where := fmt.Sprintf("signing key %d", i)
		if i == len(sources)-1 {
			where = devKeyEnv
		}
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("library: %s is not base64: %w", where, err)
		}
		if len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("library: %s is %d bytes, want %d", where, len(raw), ed25519.PublicKeySize)
		}
		out = append(out, ed25519.PublicKey(raw))
	}
	return out, nil
}
