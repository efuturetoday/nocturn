// Package catalog_test checks the PUBLISHED catalog the way a daemon reads it: over HTTP, through
// library.Store, with every check the daemon applies. Nothing here parses the JSON by hand — a test
// with its own parser would prove the file is fine and say nothing about whether the daemon can use
// it.
package catalog_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/extension"
	"github.com/efuturetoday/nocturn/internal/library"
	"github.com/efuturetoday/nocturn/internal/mcp"
	"github.com/efuturetoday/nocturn/internal/plugin"
	"github.com/efuturetoday/nocturn/internal/skill"
)

// published is the generated catalog, as it will be served.
const published = "../docs/public/catalog.json"

// src is the source tree entries are authored in.
const src = "extensions"

// TestThePublishedCatalogOffersEverySignedEntry is the drop detector: the daemon's own parser reads
// the published file, and what comes out has to match the signed folders in the source tree. An
// UNSIGNED folder is expected to be absent — the generator declines to publish one, since a remote
// daemon would drop it anyway.
func TestThePublishedCatalogOffersEverySignedEntry(t *testing.T) {
	cat := fetch(t)
	got := make([]string, 0, len(cat.Items))
	for _, it := range cat.Items {
		got = append(got, it.ID)
	}
	if diff := missing(signedEntries(t), got); len(diff) > 0 {
		t.Errorf("signed entries the daemon does not offer: %v\n"+
			"they were dropped by library.parse — regenerate with `go generate ./catalog/`", diff)
	}
}

// signedEntries are the source folders that carry a signature, and may therefore be published.
func signedEntries(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, name := range dirNames(t, src) {
		if _, err := os.Stat(filepath.Join(src, name, "extension.sig")); err == nil {
			out = append(out, name)
		}
	}
	return out
}

// TestTheDropDetectorCanSeeADrop is the counter-check for the test above. A green "nothing was
// dropped" is only worth something if a drop would actually turn it red — so tamper with one digest
// and prove the entry disappears from what the daemon offers.
func TestTheDropDetectorCanSeeADrop(t *testing.T) {
	data, err := os.ReadFile(published)
	if err != nil {
		t.Fatal(err)
	}
	var cat library.Catalog
	if err := json.Unmarshal(data, &cat); err != nil {
		t.Fatal(err)
	}
	if len(cat.Items) == 0 {
		t.Fatal("nothing published to tamper with — this check is vacuous until the tree is signed")
	}
	victim := cat.Items[0].ID
	cat.Items[0].SHA256 = strings.Repeat("0", 64)
	tampered, err := json.Marshal(cat)
	if err != nil {
		t.Fatal(err)
	}

	got := serve(t, tampered)
	for _, s := range got.Items {
		if s.ID == victim {
			t.Fatalf("entry %q survived a wrong digest; this test can no longer detect a dropped entry", victim)
		}
	}
}

// TestEveryEntryInstalls rehearses the real install of every offered entry into a scratch directory.
// extension.Install is what the daemon calls, so a payload that could not land on disk fails here,
// and each payload is then read back by the loader that will read it in a workspace.
func TestEveryEntryInstalls(t *testing.T) {
	cat := fetch(t)
	if len(cat.Items) == 0 {
		t.Fatal("nothing published — the install rehearsal is vacuous until the tree is signed")
	}
	dir := t.TempDir()
	for _, it := range cat.Items {
		// One shared directory on purpose: Install refuses a name that already exists, so two entries
		// resolving to one folder fail here rather than shadowing each other after install.
		err := extension.Install(dir, extension.Package{
			Name:           it.ID,
			Manifest:       it.Manifest,
			Skill:          it.Skill,
			PluginManifest: it.PluginManifest,
			PluginScript:   it.PluginScript,
			MCP:            it.MCP,
		})
		if err != nil {
			t.Errorf("entry %q: %v", it.ID, err)
			continue
		}
		if it.Carries(extension.PayloadSkill) {
			sk, err := skill.Parse(it.Skill, it.ID)
			if err != nil {
				t.Errorf("entry %q: its skill would be skipped: %v", it.ID, err)
			} else if sk.Description == "" {
				t.Errorf("entry %q: its skill has no description", it.ID)
			}
		}
		if it.Carries(extension.PayloadPlugin) {
			loaded, err := plugin.Load(filepath.Join(dir, it.ID))
			if err != nil {
				t.Errorf("entry %q: the plugin it installed will not load: %v", it.ID, err)
			} else if len(loaded.Manifest.Tools) == 0 {
				t.Errorf("entry %q: its plugin exposes no tools", it.ID)
			}
		}
		if it.Carries(extension.PayloadMCP) {
			if _, err := mcp.Read(dir, it.ID); err != nil {
				t.Errorf("entry %q: the server it installed will not load: %v", it.ID, err)
			}
		}
	}
}

// TestEveryEntryIsSigned is the counter-check to the rehearsal above: an unsigned or tampered entry is
// dropped by library.parse, so a green rehearsal over an EMPTY list would prove nothing.
//
// It FAILS rather than logs. A tree where nothing is signed publishes an empty shelf to every daemon
// that has not configured its own catalog — which is exactly the state a green test suite must not
// describe as fine. The fix is one command, and the message says it.
func TestEveryEntryIsSigned(t *testing.T) {
	want := dirNames(t, src)
	if len(want) == 0 {
		t.Skip("no entries in the source tree")
	}
	cat := fetch(t)
	got := make([]string, 0, len(cat.Items))
	for _, it := range cat.Items {
		got = append(got, it.ID)
	}
	if diff := missing(want, got); len(diff) > 0 {
		t.Errorf("entries the catalog does not offer: %v\n"+
			"they are unsigned, or signed against a key this build does not trust — sign them with\n"+
			"    (cd catalog && go run sign.go entryread.go -key <the project key>)\n"+
			"and regenerate with `go generate ./catalog/`. Publishing an unsigned entry would put it on\n"+
			"a shelf every remote daemon silently drops.", diff)
	}
}

// TestTheCatalogIsRegenerated catches the other half of the drift check: the committed file must be
// the one the generator would write. CI runs `go generate` and diffs, but that is a workflow step and
// this is the test somebody runs locally before pushing.
func TestTheCatalogIsRegenerated(t *testing.T) {
	data, err := os.ReadFile(published)
	if err != nil {
		t.Fatal(err)
	}
	var cat library.Catalog
	if err := json.Unmarshal(data, &cat); err != nil {
		t.Fatal(err)
	}
	for _, it := range cat.Items {
		for file, published := range map[string]string{
			"SKILL.md":    it.Skill,
			"plugin.json": it.PluginManifest,
			"plugin.js":   it.PluginScript,
			"mcp.json":    it.MCP,
		} {
			if published == "" {
				continue
			}
			body, err := os.ReadFile(filepath.Join(src, it.ID, file))
			if err != nil {
				t.Errorf("entry %q publishes a %s with no source: %v", it.ID, file, err)
				continue
			}
			if string(body) != published {
				t.Errorf("entry %q: the published %s differs from %s/%s/%s — run `go generate ./catalog/`",
					it.ID, file, src, it.ID, file)
			}
		}
	}
}

// TestTheDefaultURLIsWhereTheSiteServesIt ties the constant every daemon fetches to the place the
// docs workflow actually puts the file. Astro serves docs/public/ at the site root under its `base`,
// so the URL is derivable — and the day somebody attaches a custom domain and drops the base, this
// fails instead of every unconfigured daemon quietly 404ing.
func TestTheDefaultURLIsWhereTheSiteServesIt(t *testing.T) {
	cfg, err := os.ReadFile("../docs/astro.config.mjs")
	if err != nil {
		t.Fatal(err)
	}
	site := match(t, `site:\s*'([^']+)'`, cfg)
	base := match(t, `const base = '([^']*)'`, cfg)

	want := strings.TrimSuffix(site, "/") + base + "/catalog.json"
	if library.DefaultURL != want {
		t.Errorf("library.DefaultURL = %q, but the site serves the catalog at %q", library.DefaultURL, want)
	}
}

// match pulls the first capture of pattern out of data.
func match(t *testing.T, pattern string, data []byte) string {
	t.Helper()
	m := regexp.MustCompile(pattern).FindSubmatch(data)
	if m == nil {
		t.Fatalf("astro.config.mjs: nothing matches %s", pattern)
	}
	return string(m[1])
}

// fetch serves the published file the way a daemon reads it: over HTTP, through library.Store, with
// every check the daemon applies. Nothing here parses the JSON by hand — a test with its own parser
// would prove the file is fine and say nothing about whether the daemon can use it.
func fetch(t *testing.T) *library.Catalog {
	t.Helper()
	data, err := os.ReadFile(published)
	if err != nil {
		t.Fatalf("%s: %v (run `go generate ./catalog/`)", published, err)
	}
	return serve(t, data)
}

// serve runs one catalog document through the daemon's own Store and returns what survived.
func serve(t *testing.T, data []byte) *library.Catalog {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}))
	t.Cleanup(srv.Close)

	store := library.New(library.Source{URL: srv.URL, Client: srv.Client()}, t.TempDir(), nil)
	cat, err := store.Catalog(context.Background(), true)
	if err != nil {
		t.Fatalf("the daemon refuses the published catalog: %v", err)
	}
	return cat
}

// dirNames lists the entry directories under dir.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out
}

// missing returns the entries of want that got does not contain.
func missing(want, got []string) []string {
	have := make(map[string]bool, len(got))
	for _, g := range got {
		have[g] = true
	}
	var out []string
	for _, w := range want {
		if !have[w] {
			out = append(out, w)
		}
	}
	return out
}

// A published name is a security principal and a tool-name prefix ("<name>_<tool>" must stay inside a
// provider's 64 characters). The check belongs where a name is CHOSEN — here, at publish time —
// rather than in discovery, where a cap would silently stop reading folders that already exist.
func TestAPublishedNameFitsAToolPrefix(t *testing.T) {
	const max = 32
	for _, name := range dirNames(t, src) {
		if len(name) > max {
			t.Errorf("%q: a published name must be at most %d characters", name, max)
		}
	}
}
