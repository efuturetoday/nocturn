package library_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/efuturetoday/nocturn/internal/library"
)

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
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

func catalogJSON(t *testing.T, skills []map[string]any, servers []map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"schemaVersion": 1, "version": "2026-08-10", "skills": skills, "mcp": servers,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func goodSkill() map[string]any {
	return map[string]any{
		"id": "deploy", "title": "Deploy", "description": "ships things",
		"folder": "deploy", "body": skillBody, "sha256": digest(skillBody),
	}
}

// One malformed entry must not take the catalog down — its absence is fail-closed, since an item
// that is not offered cannot be installed.
func TestCatalog_DropsBadEntriesKeepsTheRest(t *testing.T) {
	bad := []map[string]any{
		goodSkill(),
		{"id": "", "folder": "x", "body": skillBody, "sha256": digest(skillBody)},         // no id
		{"id": "y", "folder": "Bad Name", "body": skillBody, "sha256": digest(skillBody)}, // invalid folder
		{"id": "z", "folder": "z", "body": skillBody, "sha256": digest("something else")}, // digest mismatch
		{"id": "w", "folder": "w", "body": skillBody},                                     // no digest at all
	}
	servers := []map[string]any{
		{"id": "acme", "name": "acme", "url": "https://acme.example/mcp"},
		{"id": "plain", "name": "plain", "url": "http://plain.example/mcp"}, // not https
		{"id": "shout", "name": "Shout", "url": "https://shout.example/mcp"},
	}
	store, _ := serveCatalog(t, catalogJSON(t, bad, servers))

	cat, err := store.Catalog(t.Context(), false)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(cat.Skills) != 1 || cat.Skills[0].ID != "deploy" {
		t.Fatalf("skills = %+v, want only deploy", cat.Skills)
	}
	if len(cat.MCP) != 1 || cat.MCP[0].ID != "acme" {
		t.Fatalf("mcp = %+v, want only acme", cat.MCP)
	}
}

// A catalog announcing a schema this build does not read is refused whole. Half-understanding a
// document that decides what gets installed is worse than not reading it.
func TestCatalog_RefusesAnUnknownSchema(t *testing.T) {
	body := `{"schemaVersion":99,"version":"x","skills":[],"mcp":[]}`
	store, _ := serveCatalog(t, body)
	if _, err := store.Catalog(t.Context(), false); err == nil {
		t.Fatal("a catalog with an unknown schema was accepted")
	}
}

// Unknown fields are an error, the same strictness a plugin manifest and an mcp.json get.
func TestCatalog_RefusesUnknownFields(t *testing.T) {
	body := `{"schemaVersion":1,"version":"x","skills":[],"mcp":[],"surprise":true}`
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
	if len(cat.Skills) != 1 {
		t.Fatalf("cached catalog = %+v", cat)
	}
}

// A body over the cap is a refusal, not a truncation that would then fail to parse for the wrong
// reason — the size of what a remote sends is a budget it controls, not us.
func TestCatalog_RefusesAnOversizedBody(t *testing.T) {
	huge := `{"schemaVersion":1,"version":"` + strings.Repeat("x", 9<<20) + `","skills":[],"mcp":[]}`
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
