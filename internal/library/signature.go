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

// Signing is what separates text from authority in this catalog.
//
// Everything this catalog offers is signed, and the reason is that "it is only text" stopped being a
// property of the ENTRY the day a skill could carry a manifest: that manifest declares a credential
// and the host it is bound to, which tells the host to stamp a stored secret onto every request going
// there. Deciding per entry whether a key is needed would mean two kinds of catalog skill with two
// trust paths, and a reader having to work out which one is in front of them. One rule instead: a
// signature is what a remote source is trusted through, whatever the entry happens to contain. A plugin is CODE: the sandbox contains what that code can do — no ambient authority, brokered
// imports, a memory cap and a deadline — but the manifest beside it still ASKS for authority (a cage,
// a credential bound to a host, an OAuth account). Whoever serves the catalog serves the digests too,
// so the digest cannot say who wrote that manifest. A signature can, and only if the key does not
// travel with the document — hence a key pinned in the binary.
//
// The line this draws, stated plainly: a compromised catalog host can offer text nobody vouched for;
// it cannot offer code, and it cannot offer a credential declaration. That is worth the key management, and it is why plugin entries are refused
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
	"a8yd8mk+EY3kzgxOA5XgdkCBkECz4fWpKvBQQEKJaSU=",
}

// devKeyEnv names an ADDITIONAL public key, for developing a plugin against a local catalog without a
// project key. It is opt-in per process and named as what it is: anyone who can set the environment
// of the daemon can already replace its binary.
const devKeyEnv = "NOCTURN_CATALOG_DEV_KEY"

// Signed is everything one entry's signature covers: its identity, the digest of every payload an
// install would write, the LISTING and a serial — together, in one statement.
//
// Together, because signing the parts separately would let somebody keep the artifact we signed and
// put a different declaration in front of it — and the declaration is the half that asks for the
// credential. The LISTING is in there because a person picks by it: a host that had been taken over
// could otherwise rebrand a signed mail integration as "calendar sync, no mail access" while every
// artifact stayed the one we signed. The SERIAL is in there because a signature says "we published
// these bytes", never "this is current": without something monotonic, an old and perfectly signed
// entry can be served forever, including one withdrawn for a reason. See Freshness.
type Signed struct {
	ID         string
	SHA256     string // ItemDigest: every payload, length-prefixed and labelled
	ListingSHA string
	Serial     int
}

// SignedStatement is the exact byte string an entry's signature covers. Newline-separated and
// field-labelled, so no two distinct entries can produce the same bytes.
func SignedStatement(s Signed) []byte {
	return []byte("nocturn-extension-v1\n" +
		"id=" + s.ID + "\n" +
		"sha256=" + strings.ToLower(s.SHA256) + "\n" +
		"listing=" + strings.ToLower(s.ListingSHA) + "\n" +
		"serial=" + strconv.Itoa(s.Serial) + "\n")
}

// verifyItemSignature reports whether an entry carries a signature by a key this build trusts.
//
// needed comes from the SOURCE, never from what the entry contains: everything a remote catalog
// offers is signed, so there is one kind of catalog entry rather than several with several trust
// paths. A signature that IS present is verified either way, because a wrong one means somebody tried
// and something is off.
func verifyItemSignature(it Item, needed bool) error {
	if it.Signature == "" {
		if !needed {
			return nil
		}
		return errors.New("unsigned")
	}
	sig, err := base64.StdEncoding.DecodeString(it.Signature)
	if err != nil {
		return fmt.Errorf("signature is not base64: %w", err)
	}
	msg := SignedStatement(Signed{
		ID: it.ID, SHA256: it.SHA256, ListingSHA: it.listingDigest(), Serial: it.Serial,
	})
	keys, err := trustedKeys()
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return errors.New("this build trusts no catalog signing key, so nothing can be installed")
	}
	for _, key := range keys {
		if ed25519.Verify(key, msg, sig) {
			return nil
		}
	}
	return errors.New("no trusted key signed this entry")
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
// and whoever can write them can drop a folder into extensions/ directly, which has never needed a
// signature. Demanding one there would mean minting keys to install your own plugin from your own
// file, which is the kind of rule people route around rather than follow.
type signaturePolicy bool

const (
	signaturesRequired signaturePolicy = true
	signaturesOptional signaturePolicy = false
)

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
