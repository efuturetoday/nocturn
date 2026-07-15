package skill_test

import (
	"context"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/skill"
)

func loadTool(t *testing.T) (skillInvoke func(ctx context.Context, args string) (string, error), desc, params string) {
	t.Helper()
	tl, ok := skill.Discover(scopes()).LoadTool()
	if !ok {
		t.Fatal("LoadTool: expected a tool (skills exist)")
	}
	if tl.Name != skill.LoadToolName {
		t.Fatalf("tool name = %q, want %q", tl.Name, skill.LoadToolName)
	}
	return tl.Invoke, tl.Description, string(tl.Parameters)
}

// The catalog lists visible skills in the description and constrains the name
// parameter to their enum; a model-invocation:never skill appears in NEITHER.
func TestLoadTool_CatalogAndEnum(t *testing.T) {
	_, desc, params := loadTool(t)

	for _, name := range []string{"greeter", "helper", "multiline"} {
		if !strings.Contains(desc, name) {
			t.Errorf("catalog description missing visible skill %q", name)
		}
		if !strings.Contains(params, `"`+name+`"`) {
			t.Errorf("enum missing visible skill %q", name)
		}
	}
	// The hidden skill must not leak into either surface.
	if strings.Contains(desc, "secret-skill") || strings.Contains(params, "secret-skill") {
		t.Error("a model-invocation:never skill leaked into the catalog/enum")
	}
}

// Loading a visible skill returns its body wrapped; an unknown or hidden skill
// errors (the model can't reach a hidden skill through skill.load).
func TestLoadTool_Invoke(t *testing.T) {
	invoke, _, _ := loadTool(t)
	ctx := context.Background()

	out, err := invoke(ctx, `{"name":"greeter"}`)
	if err != nil {
		t.Fatalf("load greeter: %v", err)
	}
	if !strings.Contains(out, "Say hello back") || !strings.Contains(out, `<skill name="greeter">`) {
		t.Errorf("loaded body = %q, want wrapped greeter body", out)
	}

	if _, err := invoke(ctx, `{"name":"nope"}`); err == nil {
		t.Error("unknown skill must error")
	}
	if _, err := invoke(ctx, `{"name":"secret-skill"}`); err == nil {
		t.Error("a hidden skill must not be loadable via skill.load")
	}
}

// Re-loading the same skill in a session is deduplicated (the standard's
// requirement): the second call reports "already loaded", not the body again.
func TestLoadTool_DedupPerSession(t *testing.T) {
	invoke, _, _ := loadTool(t)
	ctx := skill.WithActive(context.Background(), skill.NewActive())

	first, err := invoke(ctx, `{"name":"helper"}`)
	if err != nil || !strings.Contains(first, "Helper body") {
		t.Fatalf("first load: out=%q err=%v", first, err)
	}
	second, err := invoke(ctx, `{"name":"helper"}`)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if strings.Contains(second, "Helper body") || !strings.Contains(second, "already loaded") {
		t.Errorf("second load = %q, want an already-loaded notice, not the body", second)
	}
}

// With no visible skills, LoadTool registers nothing (no empty activation tool).
func TestLoadTool_NoneVisible(t *testing.T) {
	ix := skill.Discover([]skill.Scope{{Dir: "testdata/empty", Location: "x"}})
	if _, ok := ix.LoadTool(); ok {
		t.Error("expected no tool when no skills are visible")
	}
}
