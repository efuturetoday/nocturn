// Package library is the curated catalog a person installs skills and MCP servers from — the shop
// side of extending a workspace, as opposed to assembling a folder by hand.
//
// It is daemon-wide rather than per-workspace: the catalog is the same wherever it is installed into,
// and one fetch serves every workspace. It is also not a tool. Nothing the model says reaches it, so
// it passes no gate — the same class of host-initiated traffic as the LLM endpoint, the embedding
// endpoint and the push provider.
//
// # What trust rests on, exactly
//
// Signing is not built (Ed25519 for skills and plugins is an open item), so this package does not
// pretend it is. Two things carry the weight instead:
//
//   - ONE source, over TLS. The catalog is fetched from a single configured host and carries skill
//     bodies INLINE, so installing never fetches from a second place. A catalog listing URLs would
//     turn every listed URL into a trust anchor and the daemon into something that fetches from
//     strangers.
//   - A digest per entry, checked before anything is written. It authenticates nothing on its own —
//     whoever serves the catalog serves the digest — but it turns a truncated or garbled response
//     into a refusal instead of a half-installed skill, and it is the field a signature would be
//     computed over later.
//
// What it deliberately does NOT rest on is a person reading a skill body before installing it. The
// app shows the body, and showing it is right, but nobody spots a subtle instruction in four thousand
// tokens on a phone. The controls that actually hold are elsewhere and already built: a skill carries
// zero authority (ADR-10 — the gate reads no skills), and an installed MCP server's first call still
// asks about its host on the net axis.
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
	"github.com/efuturetoday/nocturn/internal/mcp"
)

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

// Catalog is what the remote publishes.
type Catalog struct {
	SchemaVersion int         `json:"schemaVersion"`
	Version       string      `json:"version"` // the catalog's own revision, for a client to show
	Skills        []SkillItem `json:"skills"`
	MCP           []MCPItem   `json:"mcp"`
}

// SkillItem is one installable skill. The body is inline, which is what keeps the catalog the only
// place this daemon fetches from.
type SkillItem struct {
	ID          string   `json:"id"` // stable, what an install names
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Homepage    string   `json:"homepage,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Folder      string   `json:"folder"` // the directory name to install under
	Body        string   `json:"body"`   // the whole SKILL.md, frontmatter included
	SHA256      string   `json:"sha256"` // of Body
}

// MCPItem is one installable MCP server: a declaration, never code and never a credential.
type MCPItem struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Homepage    string         `json:"homepage,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Name        string         `json:"name"` // the folder/server name to install under
	URL         string         `json:"url"`
	Auth        string         `json:"auth,omitempty"`
	OAuth       *mcp.OAuthDecl `json:"oauth,omitempty"`
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
	return &Store{src: src, cache: filepath.Join(dataDir, cacheFile), log: log.With("component", "library")}
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
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("library: bad catalog URL: %w", err)
	}
	if u.Scheme == "https" || isLoopback(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("library: catalog URL must be https (got %q)", u.Scheme)
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

// Skill returns one catalog skill by id.
func (s *Store) Skill(ctx context.Context, id string) (SkillItem, error) {
	cat, err := s.Catalog(ctx, false)
	if err != nil {
		return SkillItem{}, err
	}
	for _, it := range cat.Skills {
		if it.ID == id {
			return it, nil
		}
	}
	return SkillItem{}, fmt.Errorf("library: no skill %q", id)
}

// Server returns one catalog MCP server by id.
func (s *Store) Server(ctx context.Context, id string) (MCPItem, error) {
	cat, err := s.Catalog(ctx, false)
	if err != nil {
		return MCPItem{}, err
	}
	for _, it := range cat.MCP {
		if it.ID == id {
			return it, nil
		}
	}
	return MCPItem{}, fmt.Errorf("library: no server %q", id)
}

// fetch reads and validates the catalog.
func (s *Store) fetch(ctx context.Context) (*Catalog, error) {
	if err := checkSource(s.src.URL); err != nil {
		return nil, err
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
	return parse(data)
}

// parse decodes and validates a catalog. Unknown fields are an error, the same strictness a plugin
// manifest and an mcp.json get: a field this build cannot see is one it cannot honour.
func parse(data []byte) (*Catalog, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var cat Catalog
	if err := dec.Decode(&cat); err != nil {
		return nil, fmt.Errorf("library: catalog: %w", err)
	}
	if cat.SchemaVersion != schemaVersion {
		return nil, fmt.Errorf("library: catalog schema %d, this build reads %d", cat.SchemaVersion, schemaVersion)
	}
	cat.Skills = validSkills(cat.Skills)
	cat.MCP = validServers(cat.MCP)
	return &cat, nil
}

// validSkills keeps the entries this build can install. A bad entry is dropped, not fatal: one
// malformed row must not take a whole catalog down, and its absence is fail-closed — an item that is
// not offered cannot be installed.
func validSkills(items []SkillItem) []SkillItem {
	out := make([]SkillItem, 0, len(items))
	for _, it := range items {
		if it.ID == "" || it.Body == "" || !discovery.ValidName(it.Folder) {
			continue
		}
		if !digestMatches(it.Body, it.SHA256) {
			continue
		}
		out = append(out, it)
	}
	return out
}

// validServers keeps the declarations this build can install, checked by the same Validate the loader
// runs — so the catalog cannot offer something that would be skipped the moment it landed on disk.
func validServers(items []MCPItem) []MCPItem {
	out := make([]MCPItem, 0, len(items))
	for _, it := range items {
		if it.ID == "" {
			continue
		}
		srv := mcp.Server{Name: it.Name, URL: it.URL, Auth: it.Auth, OAuth: it.OAuth}
		if !discovery.ValidName(it.Name) || srv.Validate() != nil {
			continue
		}
		out = append(out, it)
	}
	return out
}

// digestMatches checks a body against its declared sha256.
//
// It authenticates nothing — whoever serves the catalog serves the digest too — and this comment
// exists so nobody later mistakes it for a signature. What it does is turn a truncated or corrupted
// body into a refusal rather than a half-installed skill, and it is the field a signature would be
// computed over when there is one.
func digestMatches(body, want string) bool {
	if want == "" {
		return false
	}
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:]) == strings.ToLower(want)
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
	cat, err := parse(data)
	if err != nil {
		return nil
	}
	return cat
}
