package skill_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/skill"
)

func TestDiscover(t *testing.T) {
	var diag agentkit.Diagnostics
	set, dirs := skill.Discover("testdata/skills", &diag)

	// Valid skills load; invalid (bad-name, no-desc) and non-skill dirs (notaskill) are skipped,
	// each with a diagnostic.
	if len(set) != 2 {
		t.Fatalf("loaded %d skills, want 2 (text-stats, summarize-url); got %v", len(set), keys(dirs))
	}
	if diag.Len() < 2 {
		t.Errorf("want >=2 diagnostics for the skipped skills (Bad_Name, no-desc), got %d: %v", diag.Len(), diag.All())
	}
	for _, want := range []string{"text-stats", "summarize-url"} {
		if _, ok := set[want]; !ok {
			t.Errorf("skill %q missing from set", want)
		}
		if _, ok := dirs[want]; !ok {
			t.Errorf("skill %q missing from dirs map", want)
		}
	}
	for _, bad := range []string{"Bad_Name", "no-desc"} {
		if _, ok := set[bad]; ok {
			t.Errorf("invalid skill %q was loaded", bad)
		}
	}

	// text-stats bundles files, so its body carries the resource listing (folded in at load).
	body := set["text-stats"].Body
	if !strings.Contains(body, "<skill_resources") {
		t.Errorf("text-stats body missing resource listing:\n%s", body)
	}
	for _, f := range []string{"scripts/analyze.js", "references/metrics.md"} {
		if !strings.Contains(body, f) {
			t.Errorf("resource listing missing %q", f)
		}
	}
	// summarize-url bundles nothing → no listing.
	if strings.Contains(set["summarize-url"].Body, "<skill_resources") {
		t.Errorf("summarize-url should have no resource listing")
	}
}

func TestDiscover_MissingDir(t *testing.T) {
	var diag agentkit.Diagnostics
	set, dirs := skill.Discover("testdata/does-not-exist", &diag)
	if len(set) != 0 || len(dirs) != 0 || diag.Len() != 0 {
		t.Fatalf("missing dir should yield no skills, got %d/%d, %d diags", len(set), len(dirs), diag.Len())
	}
}

func TestReadTool(t *testing.T) {
	var diag agentkit.Diagnostics
	_, dirs := skill.Discover("testdata/skills", &diag)
	tool, err := skill.ReadTool(dirs)
	if err != nil {
		t.Fatalf("ReadTool: %v", err)
	}
	ctx := context.Background()

	out, err := tool.Call(ctx, `{"name":"text-stats","path":"scripts/analyze.js"}`)
	if err != nil {
		t.Fatalf("reading a bundled file: %v", err)
	}
	if !strings.Contains(out, "function analyze") {
		t.Errorf("unexpected file content: %q", out)
	}

	if _, err := tool.Call(ctx, `{"name":"nope","path":"x"}`); err == nil {
		t.Error("reading an unknown skill should error")
	}
	if _, err := tool.Call(ctx, `{"name":"text-stats","path":"  "}`); err == nil {
		t.Error("empty path should error")
	}
}

// TestReadTool_Escape is the load-bearing security test: skill_read must NEVER read outside the
// skill directory — not via .., an absolute path, or a symlink pointing out. All three must fail
// before any byte of the secret is returned.
func TestReadTool_Escape(t *testing.T) {
	base := t.TempDir()
	skillDir := filepath.Join(base, "myskill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "ok.txt"), []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink INSIDE the skill dir that points OUT — the case lexical confinement misses.
	_ = os.Symlink(secret, filepath.Join(skillDir, "leak"))

	tool, err := skill.ReadTool(map[string]string{"myskill": skillDir})
	if err != nil {
		t.Fatalf("ReadTool: %v", err)
	}
	ctx := context.Background()

	// Sanity: an in-bounds read works.
	if out, err := tool.Call(ctx, `{"name":"myskill","path":"ok.txt"}`); err != nil || out != "inside" {
		t.Fatalf("in-bounds read failed: out=%q err=%v", out, err)
	}

	cases := []struct {
		name, path string
	}{
		{"parent-traversal", "../secret.txt"},
		{"deep-traversal", "../../../../etc/passwd"},
		{"absolute", secret},
		{"symlink-out", "leak"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := `{"name":"myskill","path":` + jsonString(tc.path) + `}`
			out, err := tool.Call(ctx, args)
			if err == nil {
				t.Fatalf("escape %q was allowed, returned %q", tc.path, out)
			}
			if strings.Contains(out, "TOP SECRET") {
				t.Fatalf("escape %q leaked the secret", tc.path)
			}
		})
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// jsonString quotes s as a JSON string literal for building tool args.
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
