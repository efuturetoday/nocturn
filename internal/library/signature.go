package library

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
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
// A plugin REMOVED from the catalog cannot be detected. An old document still containing it presents
// every entry at a serial this daemon already accepted, and nothing signed says how many entries
// there should be. Catching that needs a signature over the SET — the whole catalog, re-signed on
// every publish including one that only edits a skill — which puts the key in the path of every
// change or into CI. That is a release-process decision, and it is not made here.

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

// Signed is everything one plugin entry's signature covers.
//
// Identity, every artifact digest, the LISTING and a serial — together, in one statement. Signing the
// artifacts separately would let somebody keep a signed script and put a different manifest in front
// of it (the manifest being the half that asks for the credential), or swap the bundled skill, which
// is text that lands in the system prompt. Leaving the listing out let a host that had been taken
// over rebrand a signed plugin — "calendar sync, no mail access" over a mail plugin — while the
// artifacts stayed the ones we signed, and a person picks by that text.
//
// Serial is what makes an OLD signature insufficient. A signature says "we published these bytes",
// never "this is current": without something monotonic, a host can serve yesterday's correctly signed
// entry forever, including one withdrawn because it was found to be wrong. See Freshness.
type Signed struct {
	ID          string
	Folder      string
	ManifestSHA string
	ScriptSHA   string
	SkillSHA    string
	ListingSHA  string
	Serial      int
}

// SignedStatement is the exact byte string a plugin signature covers.
//
// A plugin with no bundled skill signs the empty digest, so "no skill" is itself signed rather than a
// gap something could be dropped into. The form is newline-separated and field-labelled, so no two
// distinct entries can produce the same bytes.
func SignedStatement(s Signed) []byte {
	return []byte("nocturn-plugin-v2\n" +
		"id=" + s.ID + "\n" +
		"folder=" + s.Folder + "\n" +
		"manifest=" + strings.ToLower(s.ManifestSHA) + "\n" +
		"script=" + strings.ToLower(s.ScriptSHA) + "\n" +
		"skill=" + strings.ToLower(s.SkillSHA) + "\n" +
		"listing=" + strings.ToLower(s.ListingSHA) + "\n" +
		"serial=" + strconv.Itoa(s.Serial) + "\n")
}

// ListingDigest is the digest of what a person READS when deciding to install: the title, the
// description, the homepage and the tags, in a fixed order.
//
// Computed from the entry itself rather than carried beside it, so a catalog cannot declare one
// listing and show another.
func ListingDigest(title, description, homepage string, tags []string) string {
	h := sha256.New()
	fmt.Fprintf(h, "title\x00%s\x00", title)
	fmt.Fprintf(h, "description\x00%s\x00", description)
	fmt.Fprintf(h, "homepage\x00%s\x00", homepage)
	for _, t := range tags {
		fmt.Fprintf(h, "tag\x00%s\x00", t)
	}
	return hex.EncodeToString(h.Sum(nil))
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
	msg := SignedStatement(Signed{
		ID: it.ID, Folder: it.Folder,
		ManifestSHA: it.ManifestSHA, ScriptSHA: it.ScriptSHA, SkillSHA: it.SkillSHA,
		ListingSHA: it.listingDigest(), Serial: it.Serial,
	})

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
