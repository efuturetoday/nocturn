// Package catalog_test proves the published catalog is installable.
//
// It is not a test of the library package — that one has its own. What it guards is the file this
// repository publishes, against the one failure mode a catalog has: an entry the daemon drops
// silently. A wrong digest, a folder name with an underscore in it, a SKILL.md without a description,
// two skills claiming one name, a server URL that is not https — every one of those makes an entry
// vanish from the shelf with no error anywhere, and the only place that can be noticed is here.
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

	"github.com/efuturetoday/nocturn/internal/library"
	"github.com/efuturetoday/nocturn/internal/mcp"
	"github.com/efuturetoday/nocturn/internal/plugin"
	"github.com/efuturetoday/nocturn/internal/skill"
)

// published is the generated catalog, as it will be served.
const published = "../docs/public/catalog.json"

// TestThePublishedCatalogOffersEverySourceEntry is the drop detector: the daemon's own parser reads
// the published file, and what comes out has to match what the source tree holds.
func TestThePublishedCatalogOffersEverySourceEntry(t *testing.T) {
	cat := fetch(t)

	wantSkills := dirNames(t, "skills")
	gotSkills := make([]string, 0, len(cat.Skills))
	for _, s := range cat.Skills {
		gotSkills = append(gotSkills, s.ID)
	}
	if diff := missing(wantSkills, gotSkills); len(diff) > 0 {
		t.Errorf("skills in catalog/skills/ that the daemon does not offer: %v\n"+
			"they were dropped by library.parse — regenerate with `go generate ./catalog/`", diff)
	}

	wantServers := fileNames(t, "mcp")
	gotServers := make([]string, 0, len(cat.MCP))
	for _, s := range cat.MCP {
		gotServers = append(gotServers, s.ID)
	}
	if diff := missing(wantServers, gotServers); len(diff) > 0 {
		t.Errorf("servers in catalog/mcp/ that the daemon does not offer: %v", diff)
	}
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
	if len(cat.Skills) == 0 {
		t.Fatal("no skills to tamper with")
	}
	victim := cat.Skills[0].ID
	cat.Skills[0].SHA256 = strings.Repeat("0", 64)
	tampered, err := json.Marshal(cat)
	if err != nil {
		t.Fatal(err)
	}

	got := serve(t, tampered)
	for _, s := range got.Skills {
		if s.ID == victim {
			t.Fatalf("skill %q survived a wrong digest; this test can no longer detect a dropped entry", victim)
		}
	}
}

// TestEverySkillInstalls rehearses the real install of every offered skill into a scratch directory.
// skill.Write is what the daemon calls, so a body that could not land on disk fails here — including
// the trap that the catalog's folder grammar allows an underscore and a skill NAME does not.
func TestEverySkillInstalls(t *testing.T) {
	cat := fetch(t)
	dir := t.TempDir()

	if len(cat.Skills) == 0 {
		t.Fatal("the catalog offers no skills at all")
	}
	for _, it := range cat.Skills {
		// One shared directory on purpose: Write refuses a name that already exists, so two skills
		// resolving to one name fail here rather than shadowing each other after install.
		e, err := skill.Write(dir, it.Folder, it.Body)
		if err != nil {
			t.Errorf("skill %q: %v", it.ID, err)
			continue
		}
		if e.Description == "" {
			t.Errorf("skill %q installed without a description", it.ID)
		}
	}
}

// TestEveryPluginInstalls rehearses the install of every offered plugin, and with it the two things
// only a plugin has: a manifest the loader must accept, and a bundled skill that has to parse into
// the same catalog a hand-written one lands in.
func TestEveryPluginInstalls(t *testing.T) {
	cat := fetch(t)
	dir := t.TempDir()

	for _, it := range cat.Plugins {
		m, err := plugin.Write(dir, it.Folder, it.Manifest, it.Script, it.Skill)
		if err != nil {
			t.Errorf("plugin %q: %v", it.ID, err)
			continue
		}
		if len(m.Tools) == 0 {
			t.Errorf("plugin %q installed with no tools", it.ID)
		}
		if it.Skill == "" {
			continue
		}
		if _, err := skill.Parse(it.Skill, it.Folder); err != nil {
			t.Errorf("plugin %q: its bundled skill would be skipped: %v", it.ID, err)
		}
	}
}

// TestEveryPluginIsSigned is the counter-check to the one above: an unsigned or tampered plugin is
// dropped by library.parse, so a green install rehearsal over an EMPTY list would prove nothing.
func TestEveryPluginIsSigned(t *testing.T) {
	want := dirNames(t, "plugins")
	if len(want) == 0 {
		t.Skip("no plugins in the catalog yet")
	}
	cat := fetch(t)
	got := make([]string, 0, len(cat.Plugins))
	for _, p := range cat.Plugins {
		got = append(got, p.ID)
	}
	if diff := missing(want, got); len(diff) > 0 {
		t.Errorf("plugins the daemon refuses to offer: %v\n"+
			"most likely unsigned or re-signed against another key — run `go run sign.go` with the project key",
			diff)
	}
}

// TestEveryServerInstalls does the same for the MCP declarations.
func TestEveryServerInstalls(t *testing.T) {
	cat := fetch(t)
	dir := t.TempDir()

	for _, it := range cat.MCP {
		err := mcp.Write(dir, mcp.Server{Name: it.Name, URL: it.URL, Auth: it.Auth, OAuth: it.OAuth})
		if err != nil {
			t.Errorf("server %q: %v", it.ID, err)
		}
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
	for _, it := range cat.Skills {
		body, err := os.ReadFile(filepath.Join("skills", it.Folder, "SKILL.md"))
		if err != nil {
			t.Errorf("skill %q is published but has no source: %v", it.ID, err)
			continue
		}
		if string(body) != it.Body {
			t.Errorf("skill %q: the published body differs from catalog/skills/%s/SKILL.md — run `go generate ./catalog/`",
				it.ID, it.Folder)
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

// fileNames lists the *.json entries under dir, skipping the underscore-prefixed ones the importer
// writes — those are candidates a person has not curated yet, and generate.go skips them too.
func fileNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !e.IsDir() && ok && !strings.HasPrefix(e.Name(), "_") && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, name)
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
