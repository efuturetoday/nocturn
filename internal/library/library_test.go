package library_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/efuturetoday/nocturn/internal/library"
)

// digest is the entry digest of a skill-only item — what the catalog publishes for a plain
// instructions entry.
func digest(s string) string {
	return library.ItemDigest(library.Item{Skill: s})
}

const skillBody = "---\nname: deploy\ndescription: ships things\n---\n\nDo the deploy.\n"

// serveCatalog stands up a catalog server and returns a Store over it, plus a counter of fetches.
func serveCatalog(t *testing.T, body string) (*library.Store, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return library.New(library.Source{URL: srv.URL}, t.TempDir(), slog.New(slog.DiscardHandler)), &hits
}

func catalogJSON(t *testing.T, items ...[]map[string]any) string {
	t.Helper()
	all := []map[string]any{}
	for _, group := range items {
		all = append(all, group...)
	}
	b, err := json.Marshal(map[string]any{
		"schemaVersion": 1, "version": "2026-08-10", "items": all,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func goodSkill() map[string]any {
	return map[string]any{
		"id": "deploy", "title": "Deploy", "description": "ships things",
		"skill": skillBody, "sha256": digest(skillBody),
	}
}

// One malformed entry must not take the catalog down — its absence is fail-closed, since an item
// that is not offered cannot be installed.
func TestCatalog_DropsBadEntriesKeepsTheRest(t *testing.T) {
	bad := []map[string]any{
		goodSkill(),
		{"id": "", "skill": skillBody, "sha256": digest(skillBody)},         // no id
		{"id": "Bad Name", "skill": skillBody, "sha256": digest(skillBody)}, // invalid id
		{"id": "z", "skill": skillBody, "sha256": digest("something else")}, // digest mismatch
		{"id": "w", "skill": skillBody},                                     // no digest at all
		{"id": "empty", "sha256": library.ItemDigest(library.Item{})},       // carries nothing
	}
	const acmeDecl = `{"url":"https://acme.example/mcp"}`
	const plainDecl = `{"url":"http://plain.example/mcp"}` // not https
	servers := []map[string]any{
		{"id": "acme", "mcp": acmeDecl, "sha256": library.ItemDigest(library.Item{MCP: acmeDecl})},
		{"id": "plain", "mcp": plainDecl, "sha256": library.ItemDigest(library.Item{MCP: plainDecl})},
	}
	store, _ := serveCatalog(t, catalogJSON(t, bad, servers))

	cat, err := store.Catalog(t.Context(), false)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	ids := make([]string, 0, len(cat.Items))
	for _, it := range cat.Items {
		ids = append(ids, it.ID)
	}
	if len(ids) != 2 || ids[0] != "deploy" || ids[1] != "acme" {
		t.Fatalf("entries = %v, want [deploy acme]", ids)
	}
}

// A catalog announcing a schema this build does not read is refused whole. Half-understanding a
// document that decides what gets installed is worse than not reading it.
func TestCatalog_RefusesAnUnknownSchema(t *testing.T) {
	body := `{"schemaVersion":99,"version":"x","items":[]}`
	store, _ := serveCatalog(t, body)
	if _, err := store.Catalog(t.Context(), false); err == nil {
		t.Fatal("a catalog with an unknown schema was accepted")
	}
}

// Unknown fields are an error, the same strictness a plugin manifest and an mcp.json get.
func TestCatalog_RefusesUnknownFields(t *testing.T) {
	body := `{"schemaVersion":1,"version":"x","items":[],"surprise":true}`
	store, _ := serveCatalog(t, body)
	if _, err := store.Catalog(t.Context(), false); err == nil {
		t.Fatal("a catalog with an unknown field was accepted")
	}
}

// Fetching is lazy and then held: opening the library twice in a minute is not two fetches. A forced
// refresh is how a person asks for one anyway.
func TestCatalog_HoldsWhatItFetchedUntilForced(t *testing.T) {
	store, hits := serveCatalog(t, catalogJSON(t, []map[string]any{goodSkill()}, nil))

	for range 3 {
		if _, err := store.Catalog(t.Context(), false); err != nil {
			t.Fatalf("Catalog: %v", err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("fetches = %d, want 1", got)
	}
	if _, err := store.Catalog(t.Context(), true); err != nil {
		t.Fatalf("forced Catalog: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("fetches after a forced refresh = %d, want 2", got)
	}
}

// Offline should mean an old catalog, not an empty one — on a phone at home that is the normal case.
func TestCatalog_FallsBackToTheCachedCopy(t *testing.T) {
	dir := t.TempDir()
	body := catalogJSON(t, []map[string]any{goodSkill()}, nil)

	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	warm := library.New(library.Source{URL: live.URL}, dir, slog.New(slog.DiscardHandler))
	if _, err := warm.Catalog(t.Context(), false); err != nil {
		t.Fatalf("warm: %v", err)
	}
	live.Close() // the host is gone from here on

	if _, err := os.Stat(filepath.Join(dir, "catalog.json")); err != nil {
		t.Fatalf("nothing was cached: %v", err)
	}

	// A fresh store over the same data dir, pointed at a host that no longer answers.
	cold := library.New(library.Source{URL: live.URL}, dir, slog.New(slog.DiscardHandler))
	cat, err := cold.Catalog(t.Context(), false)
	if err != nil {
		t.Fatalf("the cached catalog was not served: %v", err)
	}
	if len(cat.Items) != 1 {
		t.Fatalf("cached catalog = %+v", cat)
	}
}

// A body over the cap is a refusal, not a truncation that would then fail to parse for the wrong
// reason — the size of what a remote sends is a budget it controls, not us.
func TestCatalog_RefusesAnOversizedBody(t *testing.T) {
	huge := `{"schemaVersion":1,"version":"` + strings.Repeat("x", 9<<20) + `","items":[]}`
	store, _ := serveCatalog(t, huge)
	if _, err := store.Catalog(t.Context(), false); err == nil {
		t.Fatal("an oversized catalog was accepted")
	}
}

// With no URL the library is ABSENT, not empty — and nothing reaches out.
func TestCatalog_UnconfiguredIsAbsent(t *testing.T) {
	store := library.New(library.Source{}, t.TempDir(), slog.New(slog.DiscardHandler))
	if _, err := store.Catalog(context.Background(), false); err == nil {
		t.Fatal("an unconfigured library answered")
	}
}

// Nothing in the catalog is signed, so the channel is the whole of what says these bytes are the
// catalog: an inline skill body arrives with a digest computed by whoever served it. Plain HTTP to a
// host on the network is therefore refused rather than merely discouraged.
func TestCatalog_RefusesPlainHTTPToARemoteHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(catalogJSON(t, []map[string]any{goodSkill()}, nil)))
	}))
	t.Cleanup(srv.Close)

	// httptest serves on 127.0.0.1, which IS exempt — the exemption is what keeps `go test` and a
	// developer's own file server working. So the refusal is checked against the same server named
	// by a host that is not loopback, which resolves nowhere and must not even be attempted.
	remote := strings.Replace(srv.URL, "127.0.0.1", "catalog.example", 1)
	store := library.New(library.Source{URL: remote}, t.TempDir(), slog.New(slog.DiscardHandler))
	_, err := store.Catalog(t.Context(), false)
	if err == nil {
		t.Fatal("a plain-HTTP catalog on a remote host was accepted")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("err = %v, want it to name the scheme it requires", err)
	}
}

// Loopback is the deliberate exemption: there is no network to attack, and it is what a developer
// running a catalog on their own machine is doing.
func TestCatalog_AllowsPlainHTTPOnLoopback(t *testing.T) {
	store, _ := serveCatalog(t, catalogJSON(t, []map[string]any{goodSkill()}, nil))
	if _, err := store.Catalog(t.Context(), false); err != nil {
		t.Fatalf("a loopback catalog was refused: %v", err)
	}
}

// A redirect off the host that was configured hands the guarantee to whoever answered — which is the
// same attack as plain HTTP, arriving one hop later.
func TestCatalog_RefusesARedirectOffTheConfiguredHost(t *testing.T) {
	// The flag belongs to the REDIRECT TARGET: what must not happen is the off-host request, and the
	// only place that is observable is the host that would have served it.
	var followed atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		followed.Store(true)
		_, _ = w.Write([]byte(catalogJSON(t, []map[string]any{goodSkill()}, nil)))
	}))
	t.Cleanup(elsewhere.Close)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusFound)
	}))
	t.Cleanup(origin.Close)

	store := library.New(library.Source{URL: origin.URL}, t.TempDir(), slog.New(slog.DiscardHandler))
	if _, err := store.Catalog(t.Context(), false); err == nil {
		t.Fatal("a catalog served from a redirect target was accepted")
	}
	if followed.Load() {
		t.Error("the redirect was followed")
	}
}

// The default is what a daemon nobody configured fetches, which makes its scheme the whole of the
// catalog's authenticity — checkSource would refuse it at fetch time, but a shipped constant that
// cannot be fetched is a bug nobody discovers until the library is opened.
func TestDefaultURLIsAnHTTPSCatalog(t *testing.T) {
	u, err := url.Parse(library.DefaultURL)
	if err != nil {
		t.Fatalf("DefaultURL does not parse: %v", err)
	}
	if u.Scheme != "https" {
		t.Errorf("DefaultURL scheme = %q, want https", u.Scheme)
	}
	if path.Base(u.Path) != "catalog.json" {
		t.Errorf("DefaultURL = %q, want it to name the catalog document", library.DefaultURL)
	}
}

// A household with its own skills should not have to run a web server to install them. The catalog
// is then a path, the daemon reads it off disk, and the entries are the same ones a remote catalog
// would offer — minus the requirement to be signed, because there is no channel to authenticate.
func TestCatalog_ReadsAFileOnThisMachine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(path, []byte(catalogJSON(t, []map[string]any{goodSkill()}, nil)), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, source := range map[string]string{
		"a bare path": path,
		"a file URL":  "file://" + path,
	} {
		t.Run(name, func(t *testing.T) {
			store := library.New(library.Source{URL: source}, t.TempDir(), slog.New(slog.DiscardHandler))
			cat, err := store.Catalog(t.Context(), false)
			if err != nil {
				t.Fatalf("Catalog() = %v, want the file to be read", err)
			}
			if len(cat.Items) != 1 {
				t.Errorf("got %d skills, want the one in the file", len(cat.Items))
			}
		})
	}
}

// A path that is not there says so, rather than being reported as a network problem.
func TestCatalog_MissingFileSaysSo(t *testing.T) {
	store := library.New(library.Source{URL: filepath.Join(t.TempDir(), "nope.json")}, t.TempDir(), slog.New(slog.DiscardHandler))
	if _, err := store.Catalog(t.Context(), false); err == nil {
		t.Fatal("a missing catalog file was accepted")
	}
}
