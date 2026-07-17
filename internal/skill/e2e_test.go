package skill_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/script"
	"github.com/efuturetoday/nocturn/internal/skill"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// One end-to-end use of a bundled resource: a loaded skill names a script; the
// model fetches it with skill.read and runs it with code.run — through the SAME
// registry, so any effect inside the script would still hit the broker (this
// script is pure compute). Proves the read→run bridge over the real QuickJS
// interpreter. (Reading references/templates is the same skill.read, just fed to
// the model as context instead of to code.run.)
func TestSkill_ReadResourceThenCodeRun_E2E(t *testing.T) {
	ix := skill.Discover(scopes())
	runner := script.New(tool.NewRegistry()) // code.run over an empty effect registry
	reg := tool.NewRegistry().AddMany([]tool.Tool{ix.ReadTool(), runner.Tool()}...)

	act := skill.NewActive()
	act.Mark("scripted") // the skill is loaded, so its files are readable
	ctx := skill.WithActive(context.Background(), act)

	src, err := reg.Invoke(ctx, skill.ReadToolName, `{"name":"scripted","path":"scripts/hello.js"}`)
	if err != nil {
		t.Fatalf("skill.read: %v", err)
	}

	args, _ := json.Marshal(map[string]string{"source": src})
	out, err := reg.Invoke(ctx, "code.run", string(args))
	if err != nil {
		t.Fatalf("code.run: %v", err)
	}
	if !strings.Contains(out, "hello from the bundled script") {
		t.Fatalf("code.run output = %q, want the bundled script's print", out)
	}
}
