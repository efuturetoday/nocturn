// Package library is the curated catalog a person installs extensions from — the shop side of
// extending a workspace, as opposed to assembling a folder by hand.
//
// It is daemon-wide rather than per-workspace: the catalog is the same wherever it is installed into,
// and one fetch serves every workspace. It is also not a tool. Nothing the model says reaches it, so
// it passes no gate — the same class of host-initiated traffic as the LLM endpoint, the embedding
// endpoint and the push provider.
//
// ONE ENTRY per installable thing, carrying whatever it brings: instructions, code, a server
// declaration, or several at once. What a household installs is a capability, not a delivery
// mechanism — and three separate lists let somebody take the server and leave behind the instructions
// that say when to use it.
//
// # What trust rests on, exactly
//
// Three things, in this order:
//
//   - A SIGNATURE per entry, required from a remote source. Ed25519 over identity, the digest of
//     every payload an install would write, the listing a person picks by, and a serial — all in one
//     statement (see signature.go), verified against a key compiled into the binary. Signing the
//     parts separately would let somebody keep the artifact we signed and put a different
//     declaration in front of it, and the declaration is the half that asks for a credential.
//   - ONE source, over TLS, with every payload INLINE, so installing never fetches from a second
//     place. A catalog listing URLs would turn every listed URL into a trust anchor and the daemon
//     into something that fetches from strangers.
//   - A digest per entry, checked before anything is written. On its own it authenticates nothing —
//     whoever serves the catalog serves the digest — but it turns a truncated or garbled response
//     into a refusal instead of a half-installed extension, and it is what the signature covers.
//
// A catalog read off THIS machine (a file path, or loopback) needs no signature: there is no channel
// for one to substitute for, and whoever can write that file could drop the folder into the
// extensions tree directly.
//
// What none of this rests on is a person reading a skill body before installing it. The app shows the
// body, and showing it is right, but nobody spots a subtle instruction in four thousand tokens on a
// phone. The controls that actually hold are elsewhere: a skill body carries zero authority (ADR-10 —
// the gate reads no skills), an installed server's first call still asks about its host on the net
// axis, and a credential an entry declares is bound to a host, owned by that extension, and gone when
// it is removed.
package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/internal/discovery"
	"github.com/efuturetoday/nocturn/internal/extension"
	"github.com/efuturetoday/nocturn/internal/mcp"
	"github.com/efuturetoday/nocturn/internal/plugin"
)

// DefaultURL is the catalog a daemon uses when nobody configured another one: the curated one this
// project publishes with its documentation site, generated from catalog/ in this repository.
//
// A default rather than nothing, because an empty shelf teaches a person that the library is not
// worth opening, and the alternative was every user hosting a JSON file to have any skills at all. It
// costs no traffic until somebody opens the library — New fetches nothing — and pointing the variable
// somewhere else, or switching it off, stays one environment variable away.
const DefaultURL = "https://efuturetoday.github.io/nocturn/catalog.json"

const (
	// schemaVersion is the catalog shape this build understands. A catalog announcing a different one
	// is refused whole rather than read selectively: a field this build cannot see is a field it
	// cannot honour, and half-understanding a security-relevant document is worse than not reading it.
	schemaVersion = 1

	// maxCatalogBytes caps the response. Skill bodies ride inline, so the catalog is the largest thing
	// this daemon fetches — and an unbounded read from a remote host is a memory budget somebody else
	// controls.
	maxCatalogBytes = 8 << 20

	// fetchTimeout bounds one catalog fetch end to end.
	fetchTimeout = 10 * time.Second

	// minRefresh is how long a fetched catalog is served from memory before a list will go out again.
	// A person opening the library twice in a minute is not asking for two fetches.
	minRefresh = 15 * time.Minute

	// cacheFile holds the last good catalog beside the other daemon-wide state, so the library is
	// browsable with no network — on a phone at home that is the normal case, not the exception.
	cacheFile = "catalog.json"

	// maxRedirects bounds a redirect chain. Well below net/http's own default of 10, because a catalog
	// that needs more than a couple of hops to reach is not a catalog anyone configured on purpose.
	maxRedirects = 3
)

// Catalog is what the remote publishes: one list, because one entry is one installable thing.
//
// Three lists was the older shape, and it made the delivery mechanism the unit of choice: a person
// installing "GitHub" had to find the server, then the skill that says when to use it, and could
// easily take one and not the other. What a household picks is a capability; what it carries is this
// entry's business.
type Catalog struct {
	SchemaVersion int    `json:"schemaVersion"`
	Version       string `json:"version"` // the catalog's own revision, for a client to show
	Items         []Item `json:"items"`
}

// Item is one installable extension: what it is called, what it declares, and every payload it
// carries — all inline, which is what keeps the catalog the only place this daemon fetches from.
//
// The payloads are optional and combinable. A folder with a SKILL.md is instructions; with a
// plugin.json + plugin.js it is code in the sandbox; with an mcp.json it is a remote server; with all
// three it is one integration that brings its own explanation. Each payload keeps its own trust rule
// — text is read, code is sandboxed, a host is gated — and the SIGNATURE covers them together, so
// nobody can keep the artifact we signed and put a different declaration in front of it.
type Item struct {
	ID          string   `json:"id"` // stable, what an install names; also the folder
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Homepage    string   `json:"homepage,omitempty"`
	Tags        []string `json:"tags,omitempty"`

	// Manifest is the shared declaration (manifest.json): the config a human supplies and the
	// credentials the host injects. Empty means the extension asks for nothing.
	Manifest string `json:"manifest,omitempty"`
	// Skill is the whole SKILL.md, frontmatter included.
	Skill string `json:"skill,omitempty"`
	// PluginManifest and PluginScript are plugin.json and plugin.js.
	PluginManifest string `json:"plugin_manifest,omitempty"`
	PluginScript   string `json:"plugin_script,omitempty"`
	// MCP is the server declaration (mcp.json), written verbatim.
	MCP string `json:"mcp,omitempty"`

	// SHA256 covers every part above, each length-prefixed and labelled, so no part can be moved
	// across a boundary undetected. It is what the signature is computed over.
	SHA256 string `json:"sha256"`
	// Serial is this entry's revision, and it only ever goes up. A signature says "we published these
	// bytes", never "this is current" — without something monotonic a host can serve an old, correctly
	// signed entry forever, including one withdrawn because it turned out to be wrong. See freshness.
	Serial int `json:"serial"`
	// Signature is Ed25519 over SignedItemStatement, base64. Required from a remote source: an entry
	// this build cannot verify is not offered. See signature.go.
	Signature string `json:"signature"`
}

// Carries reports whether this entry brings the given payload.
func (it Item) Carries(p extension.Payload) bool {
	switch p {
	case extension.PayloadSkill:
		return it.Skill != ""
	case extension.PayloadPlugin:
		return it.PluginManifest != "" && it.PluginScript != ""
	case extension.PayloadMCP:
		return it.MCP != ""
	}
	return false
}

// listingDigest is the digest of this entry's own listing fields — what a person reads when deciding
// to install. It is recomputed rather than carried, so a catalog cannot sign one listing and show
// another.
func (it Item) listingDigest() string {
	return ListingDigest(it.Title, it.Description, it.Homepage, it.Tags)
}

// Source is where a catalog comes from. Split out so a test can serve one without a network, and so
// the daemon can be pointed elsewhere.
type Source struct {
	URL    string
	Client *http.Client
}

// Store fetches, caches and serves the catalog. Its zero value is not usable; use New.
type Store struct {
	src   Source
	cache string // path of the on-disk copy
	log   *slog.Logger

	seen *freshness // the highest serial accepted per plugin, so a signed entry cannot go backwards

	mu      sync.Mutex
	catalog *Catalog
	fetched time.Time
}

// New builds a Store over src, caching under dataDir. The catalog is NOT fetched here: a daemon that
// phones home while starting is a different product, and nothing needs the catalog until somebody
// opens the library.
func New(src Source, dataDir string, log *slog.Logger) *Store {
	if src.Client == nil {
		src.Client = &http.Client{Timeout: fetchTimeout}
	}
	if src.Client.CheckRedirect == nil {
		// A copy, because the client belongs to whoever passed it — a test may hand the same one to
		// two Stores, and reaching into it would be a side effect nobody asked for. Everything that
		// makes it that client (its Transport, Jar and Timeout) comes along.
		c := *src.Client
		c.CheckRedirect = sameOrigin
		src.Client = &c
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Store{
		src:   src,
		cache: filepath.Join(dataDir, cacheFile),
		seen:  openFreshness(filepath.Join(dataDir, serialFile), log),
		log:   log.With("component", "library"),
	}
}

// sameOrigin refuses a redirect that leaves the scheme or host the catalog was asked for.
//
// The transport is the whole of the catalog's authenticity — nothing is signed (§9 point 3), so TLS
// to a named host is what says these bytes are the catalog. A redirect that walks https to http, or
// to another host, hands that guarantee to whoever answered; and what they would be handing back is
// an inline skill body with a digest computed over their own text, or an MCP declaration naming their
// own server.
func sameOrigin(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("library: stopped after %d redirects", maxRedirects)
	}
	first := via[0].URL
	if req.URL.Scheme != first.Scheme || req.URL.Host != first.Host {
		return fmt.Errorf("library: refusing redirect from %s://%s to %s://%s",
			first.Scheme, first.Host, req.URL.Scheme, req.URL.Host)
	}
	return nil
}

// checkSource refuses a catalog URL that is not HTTPS, unless it is loopback.
//
// Loopback is exempt because there is no network to attack: `go test` serves a catalog from httptest,
// and a developer runs one from a file server on 127.0.0.1. Every other host must be HTTPS, for the
// reason sameOrigin gives — an unsigned catalog is only as trustworthy as the channel it arrived on.
func checkSource(raw string) error {
	if _, ok := localPath(raw); ok {
		return nil // a file on this machine: no transport, nothing to secure
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("library: bad catalog URL: %w", err)
	}
	if u.Scheme == "https" || isLoopback(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("library: catalog URL must be https, or a path to a file (got %q)", u.Scheme)
}

// isLoopback reports whether host names this machine.
//
// A LITERAL only. A name that merely resolves to 127.0.0.1 is not exempt: what the exemption is for
// is a developer typing an address they can see, and resolution is exactly the step an attacker gets
// to influence.
func isLoopback(host string) bool {
	// url.Parse preserves the case it was given, and a host is case-insensitive — so http://LOCALHOST
	// has to mean what http://localhost means, or the exemption depends on how somebody typed it.
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ErrUnconfigured is returned when no catalog URL is set — the library is then simply absent, the
// same way knowledge_search is absent without an embedder.
var ErrUnconfigured = errors.New("library: no catalog configured")

// Catalog returns the catalog, fetching it if what is held is missing or stale.
//
// A fetch that fails falls back to the last good copy — the on-disk cache if memory has none. Being
// offline should mean an old catalog, not an empty one: the entries a person is most likely to want
// are the ones they saw last time.
func (s *Store) Catalog(ctx context.Context, force bool) (*Catalog, error) {
	if s.src.URL == "" {
		return nil, ErrUnconfigured
	}
	s.mu.Lock()
	held, at := s.catalog, s.fetched
	s.mu.Unlock()

	if held != nil && !force && time.Since(at) < minRefresh {
		return held, nil
	}

	fetched, err := s.fetch(ctx)
	if err == nil {
		s.mu.Lock()
		s.catalog, s.fetched = fetched, time.Now()
		s.mu.Unlock()
		s.save(fetched)
		return fetched, nil
	}

	if held != nil {
		s.log.Warn("catalog fetch failed — serving the copy already held", "err", err)
		return held, nil
	}
	if cached := s.load(); cached != nil {
		s.log.Warn("catalog fetch failed — serving the cached copy", "err", err)
		s.mu.Lock()
		// Stamp fetched as well, or the cached copy is born stale: every later call would see a zero
		// time, decide it must refresh, and pay for another failed network round trip before handing
		// back the same bytes. force=true stays the way to ask for a retry on purpose.
		s.catalog, s.fetched = cached, time.Now()
		s.mu.Unlock()
		return cached, nil
	}
	return nil, err
}

// Item returns one catalog entry by id.
func (s *Store) Item(ctx context.Context, id string) (Item, error) {
	cat, err := s.Catalog(ctx, false)
	if err != nil {
		return Item{}, err
	}
	for _, it := range cat.Items {
		if it.ID == id {
			return it, nil
		}
	}
	return Item{}, fmt.Errorf("library: no entry %q", id)
}

// fetch reads and validates the catalog.
func (s *Store) fetch(ctx context.Context) (*Catalog, error) {
	if err := checkSource(s.src.URL); err != nil {
		return nil, err
	}
	if path, ok := localPath(s.src.URL); ok {
		return s.readFile(path)
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.src.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.src.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("library: catalog returned %s", resp.Status)
	}

	// LimitReader with one byte to spare, so a body AT the cap is a refusal rather than a silent
	// truncation that would then fail to parse for the wrong reason.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxCatalogBytes {
		return nil, fmt.Errorf("library: catalog exceeds %d bytes", maxCatalogBytes)
	}
	return parse(data, s.log, s.signaturePolicy(), s.seen)
}

// readFile reads a catalog off this machine's disk.
//
// A household with its own skills should not have to run a web server to install them: the file sits
// on the same host as the workspaces, and whoever can write it can already drop a folder into
// extensions/ directly. So a path is a first-class catalog source, and it is the one shape
// where the transport guarantees are not merely relaxed but absent — hence signaturesOptional below.
func (s *Store) readFile(path string) (*Catalog, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("library: catalog file: %w", err)
	}
	if info.Size() > maxCatalogBytes {
		return nil, fmt.Errorf("library: catalog exceeds %d bytes", maxCatalogBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("library: catalog file: %w", err)
	}
	return parse(data, s.log, s.signaturePolicy(), s.seen)
}

// parse decodes and validates a catalog. Unknown fields are an error, the same strictness a plugin
// manifest and an mcp.json get: a field this build cannot see is one it cannot honour.
//
// log is where dropped entries are named. Dropping is right — one bad row must not take a catalog
// down — but doing it in silence is not: the entry is simply absent from the shelf, with nothing
// anywhere saying why, and "my skill is not in the library" is then a question nobody can answer.
func parse(data []byte, log *slog.Logger, signing signaturePolicy, seen *freshness) (*Catalog, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var cat Catalog
	if err := dec.Decode(&cat); err != nil {
		return nil, fmt.Errorf("library: catalog: %w", err)
	}
	if cat.SchemaVersion != schemaVersion {
		return nil, fmt.Errorf("library: catalog schema %d, this build reads %d", cat.SchemaVersion, schemaVersion)
	}
	cat.Items = validItems(cat.Items, log, signing, seen)
	return &cat, nil
}

// validItems keeps the entries this build can install. A bad entry is dropped, not fatal: one
// malformed row must not take a whole catalog down, and its absence is fail-closed — an item that is
// not offered cannot be installed. Every drop is logged with its reason, because "my skill is not in
// the library" is otherwise a question nobody can answer.
//
// A plugin payload additionally runs through the very parser the loader uses, so the catalog cannot
// offer code that would be skipped the moment it landed on disk — and what a client renders as "this
// is what it may reach" is what the daemon will read back.
func validItems(items []Item, log *slog.Logger, signing signaturePolicy, seen *freshness) []Item {
	out := make([]Item, 0, len(items))
	for _, it := range items {
		switch {
		case !discovery.ValidName(it.ID):
			// The id is the folder, the credential owner and the shard key, and it goes into the
			// signed statement between newline-delimited fields.
			log.Warn("catalog entry dropped", "reason", "invalid id", "id", it.ID)
		case !it.Carries(extension.PayloadSkill) && !it.Carries(extension.PayloadPlugin) && !it.Carries(extension.PayloadMCP):
			log.Warn("catalog entry dropped", "id", it.ID, "reason", "carries nothing installable")
		case (it.PluginManifest == "") != (it.PluginScript == ""):
			log.Warn("catalog entry dropped", "id", it.ID, "reason", "half a plugin (a manifest without a script, or the reverse)")
		case it.SHA256 == "" || ItemDigest(it) != strings.ToLower(it.SHA256):
			log.Warn("catalog entry dropped", "id", it.ID, "reason", "sha256 does not match what would be installed")
		case it.Serial < 0:
			log.Warn("catalog entry dropped", "id", it.ID, "reason", "negative serial")
		default:
			// The signature first, and only then the payloads: refusing an entry nobody vouched for
			// before parsing what it declares keeps the parser off unvouched bytes.
			if err := verifyItemSignature(it, signing == signaturesRequired); err != nil {
				log.Warn("catalog entry dropped", "id", it.ID, "reason", err)
				continue
			}
			// Then freshness, which a signature cannot answer: this one is genuine and may still be
			// yesterday's — including one withdrawn for a reason.
			if it.Signature != "" {
				if err := seen.checkSerial(it.ID, it.Serial); err != nil {
					log.Warn("catalog entry dropped", "id", it.ID, "reason", err)
					continue
				}
			}
			if err := checkPayloads(it); err != nil {
				log.Warn("catalog entry dropped", "id", it.ID, "reason", err)
				continue
			}
			if it.Signature != "" {
				seen.acceptSerial(it.ID, it.Serial)
			}
			out = append(out, it)
		}
	}
	return out
}

// checkPayloads runs each payload through the parser that will read it on disk: the shared
// declaration, the plugin manifest, the server declaration. What the catalog offers and what the
// workspace would accept cannot then drift apart.
//
// Strict about identity: a payload naming something other than the entry would install as one thing
// and call itself another, and the folder is what owns the credential.
func checkPayloads(it Item) error {
	if it.Manifest != "" {
		if _, err := extension.ParseDecl([]byte(it.Manifest)); err != nil {
			return err
		}
	}
	if it.Carries(extension.PayloadPlugin) {
		var m plugin.Manifest
		dec := json.NewDecoder(strings.NewReader(it.PluginManifest))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&m); err != nil {
			return fmt.Errorf("plugin manifest: %w", err)
		}
		if err := m.Validate(); err != nil {
			return err
		}
		if m.Name != it.ID {
			return fmt.Errorf("plugin manifest names %q, but the entry is %q", m.Name, it.ID)
		}
	}
	if it.Carries(extension.PayloadMCP) {
		srv, err := mcp.Parse([]byte(it.MCP), it.ID)
		if err != nil {
			return err
		}
		if err := srv.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// ItemDigest covers every part an install would write, each labelled and length-prefixed so no byte
// can move across a boundary undetected.
//
// Plain concatenation would be malleable: the same byte string split differently hashes the same, so
// a catalog host could serve a manifest as trailing body text — identical digest, credential
// declaration silently gone (or, the other way round, appeared). This is the field the signature is
// computed over, so the malleability would be inherited by the signature.
func ItemDigest(it Item) string {
	h := sha256.New()
	for _, part := range []struct{ label, body string }{
		{"manifest", it.Manifest},
		{"skill", it.Skill},
		{"plugin_manifest", it.PluginManifest},
		{"plugin_script", it.PluginScript},
		{"mcp", it.MCP},
	} {
		fmt.Fprintf(h, "%s\x00%d\x00", part.label, len(part.body))
		h.Write([]byte(part.body))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// save writes the catalog beside the other daemon-wide state. Best-effort: a failed write costs the
// offline copy, never the catalog in hand.
func (s *Store) save(cat *Catalog) {
	data, err := json.Marshal(cat)
	if err != nil {
		return
	}
	tmp := s.cache + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		s.log.Warn("caching the catalog", "err", err)
		return
	}
	if err := os.Rename(tmp, s.cache); err != nil {
		s.log.Warn("caching the catalog", "err", err)
	}
}

// load reads the cached catalog, or nil if there is none this build can read.
func (s *Store) load() *Catalog {
	data, err := os.ReadFile(s.cache)
	if err != nil {
		return nil
	}
	// The cached copy came from the source this Store is pointed at, so it is held to that source's
	// rule — a catalog cached from a remote host may not shed its signatures by going through disk.
	cat, err := parse(data, s.log, s.signaturePolicy(), s.seen)
	if err != nil {
		return nil
	}
	return cat
}

// signaturePolicy is the rule this Store's source is held to. See signature.go.
func (s *Store) signaturePolicy() signaturePolicy {
	if _, local := localPath(s.src.URL); local {
		return signaturesOptional
	}
	if u, err := url.Parse(s.src.URL); err == nil && isLoopback(u.Hostname()) {
		return signaturesOptional
	}
	return signaturesRequired
}

// localPath reports whether the source names a file on this machine, and where. A "file:" URL or a
// bare path — anything without a scheme, which is what a person types when they mean a file.
//
// A ONE-LETTER scheme is a Windows drive, not a scheme: `C:\nocturn\catalog.json` parses as
// scheme "c", and without this it would be refused as "not https" on the one platform where that is
// the ordinary way to write a path. No registered URI scheme is a single character, and this build
// ships windows binaries.
func localPath(raw string) (string, bool) {
	if rest, ok := strings.CutPrefix(raw, "file://"); ok {
		return rest, true
	}
	if rest, ok := strings.CutPrefix(raw, "file:"); ok {
		return rest, true
	}
	u, err := url.Parse(raw)
	if err == nil && len(u.Scheme) > 1 {
		return "", false // http, https, or something this build will refuse
	}
	return raw, raw != ""
}
