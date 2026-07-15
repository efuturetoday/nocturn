package skill_test

import (
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/skill"
)

func scopes() []skill.Scope {
	return []skill.Scope{
		{Dir: "testdata/ws", Location: "workspace"},
		{Dir: "testdata/user", Location: "user"},
	}
}

func names(ix *skill.Index) map[string]skill.Skill {
	m := map[string]skill.Skill{}
	for _, s := range ix.Skills() {
		m[s.Name] = s
	}
	return m
}

func diagCount(ix *skill.Index, level skill.Level) int {
	n := 0
	for _, d := range ix.Diags {
		if d.Level == level {
			n++
		}
	}
	return n
}

// Discovery loads valid skills across scopes, skips the invalid ones, and lets
// the earlier scope win a name collision (workspace shadows user).
func TestDiscover_LoadsSkipsAndShadows(t *testing.T) {
	ix := skill.Discover(scopes())
	got := names(ix)

	// secret-skill is discovered too — model-invocation:never only hides it from
	// the skill.load catalog, it does not stop discovery.
	want := []string{"greeter", "quoted-colon", "multiline", "renamed", "helper", "secret-skill", "scripted"}
	for _, n := range want {
		if _, ok := got[n]; !ok {
			t.Errorf("expected skill %q to load", n)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("loaded %d skills %v, want %d %v", len(got), keys(got), len(want), want)
	}

	// no-desc and nofrontmatter are skipped; notaskill is silently ignored.
	if _, ok := got["no-desc"]; ok {
		t.Error("no-desc has no description and must be skipped")
	}

	// The workspace greeter wins over the user greeter (shadowing).
	if d := got["greeter"].Description; !strings.HasPrefix(d, "Greet the user warmly") {
		t.Errorf("greeter description = %q, want the workspace one", d)
	}
	if got["greeter"].Location != "workspace" {
		t.Errorf("greeter loaded from %q, want workspace (earlier scope wins)", got["greeter"].Location)
	}

	// Diagnostics: >=2 skips (no-desc, nofrontmatter), >=2 warns (name mismatch,
	// shadowed user greeter).
	if diagCount(ix, skill.Skip) < 2 {
		t.Errorf("want >=2 skip diagnostics, got %d: %v", diagCount(ix, skill.Skip), ix.Diags)
	}
	if diagCount(ix, skill.Warn) < 2 {
		t.Errorf("want >=2 warn diagnostics, got %d: %v", diagCount(ix, skill.Warn), ix.Diags)
	}
}

// Frontmatter fields decode via yaml.v3 — including a quoted colon in the
// description and a multi-line block-scalar description (neither of which a
// naive line parser would handle), plus metadata and license.
func TestDiscover_FrontmatterFields(t *testing.T) {
	got := names(skill.Discover(scopes()))

	if g := got["greeter"]; g.License != "MIT" || g.Metadata["nocturn.model-invocation"] != "always" {
		t.Errorf("greeter frontmatter = license %q, metadata %v", g.License, g.Metadata)
	}
	if d := got["quoted-colon"].Description; !strings.Contains(d, "when:") {
		t.Errorf("quoted-colon description lost its colon: %q", d)
	}
	m := got["multiline"]
	if !strings.Contains(m.Description, "multi-line") || !strings.Contains(m.Description, "two lines") {
		t.Errorf("multiline description did not span lines: %q", m.Description)
	}
	if m.Compatibility != "Requires network access" {
		t.Errorf("multiline compatibility = %q", m.Compatibility)
	}
}

// Body reads the Markdown after the frontmatter on demand — not the frontmatter.
func TestSkill_Body(t *testing.T) {
	g, ok := skill.Discover(scopes()).Get("greeter")
	if !ok {
		t.Fatal("greeter not found")
	}
	body, err := g.Body()
	if err != nil {
		t.Fatalf("Body: %v", err)
	}
	if !strings.Contains(body, "Say hello back") {
		t.Errorf("body = %q, want the markdown body", body)
	}
	if strings.Contains(body, "description:") {
		t.Error("body must not include the frontmatter")
	}
}

// A missing scope directory is not an error — it just contributes nothing.
func TestDiscover_MissingScopeIgnored(t *testing.T) {
	ix := skill.Discover([]skill.Scope{{Dir: "testdata/does-not-exist", Location: "x"}})
	if ix.Len() != 0 {
		t.Fatalf("missing scope yielded %d skills", ix.Len())
	}
}

func keys(m map[string]skill.Skill) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
