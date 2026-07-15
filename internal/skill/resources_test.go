package skill_test

import (
	"context"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/skill"
)

func readTool(t *testing.T) func(ctx context.Context, args string) (string, error) {
	t.Helper()
	rt := skill.Discover(scopes()).ReadTool()
	if rt.Name != skill.ReadToolName {
		t.Fatalf("tool name = %q, want %q", rt.Name, skill.ReadToolName)
	}
	return rt.Invoke
}

// skill.read returns a bundled file only for an ALREADY-loaded skill; before the
// skill is loaded (not in the activation set) it refuses — so it can't be a
// generic file probe.
func TestReadTool_RequiresActivation(t *testing.T) {
	invoke := readTool(t)

	// Not loaded → refused.
	ctx := skill.WithActive(context.Background(), skill.NewActive())
	if _, err := invoke(ctx, `{"name":"scripted","path":"scripts/hello.js"}`); err == nil {
		t.Fatal("reading a file of an unloaded skill must be refused")
	}

	// After loading (marked active) → readable.
	act := skill.NewActive()
	act.Mark("scripted")
	ctx = skill.WithActive(context.Background(), act)
	out, err := invoke(ctx, `{"name":"scripted","path":"scripts/hello.js"}`)
	if err != nil || !strings.Contains(out, "hello from the bundled script") {
		t.Fatalf("read scripts/hello.js: out=%q err=%v", out, err)
	}
}

// A path that escapes the skill directory is a hard error before any read.
func TestReadTool_ConfinesToSkillDir(t *testing.T) {
	invoke := readTool(t)
	act := skill.NewActive()
	act.Mark("scripted")
	ctx := skill.WithActive(context.Background(), act)

	for _, p := range []string{"../greeter/SKILL.md", "../../ws/greeter/SKILL.md", "scripts/../../greeter/SKILL.md"} {
		if _, err := invoke(ctx, `{"name":"scripted","path":"`+p+`"}`); err == nil ||
			!strings.Contains(err.Error(), "escape") && !strings.Contains(err.Error(), "outside") {
			t.Errorf("escape %q: err = %v, want a confinement error", p, err)
		}
	}
}

// skill.load advertises a skill's bundled files (non-eager) so the model knows
// what it may skill.read — without loading them.
func TestLoadTool_ListsResources(t *testing.T) {
	tl, ok := skill.Discover(scopes()).LoadTool()
	if !ok {
		t.Fatal("no load tool")
	}
	out, err := tl.Invoke(context.Background(), `{"name":"scripted"}`)
	if err != nil {
		t.Fatalf("load scripted: %v", err)
	}
	if !strings.Contains(out, "<skill_resources") || !strings.Contains(out, "scripts/hello.js") {
		t.Errorf("load output missing resource listing: %q", out)
	}
	// A skill with no bundled files gets no listing.
	out, _ = tl.Invoke(context.Background(), `{"name":"greeter"}`)
	if strings.Contains(out, "<skill_resources") {
		t.Errorf("greeter has no resources but got a listing: %q", out)
	}
}
