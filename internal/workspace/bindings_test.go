package workspace_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// A skill's declared credential is what makes a REST API reachable by the MODEL rather than by a
// plugin: the skill's manifest names the credential and the host it binds to, its config.json supplies
// the address the household actually uses, and the value lives in the skill's own encrypted shard.
// internal/tools proves the injection itself; what this proves is the seam above it — that a
// credential nobody ever mentions in a prompt arrives as a header on the model's own http_read.
//
// It is the whole basis for reaching a household server, Gmail, Calendar or Graph from a skill: the
// skill writes URLs, the host attaches the account.
func TestASkillsCredentialReachesTheModelsHTTPTool(t *testing.T) {
	var gotAuth string
	var once sync.Once
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { gotAuth = r.Header.Get("Authorization") })
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[]}`))
	}))
	defer api.Close()

	dir := t.TempDir()
	writeSkillExtension(t, dir, "house", api.URL)

	// The vault has to be unlocked for any of this to exist: a locked workspace has no injector at
	// all, which is the fail-closed side of the same design.
	master := testMaster(t)
	seedShard(t, master, dir, "test", "extensions/house",
		"ext:house@"+strings.TrimPrefix(api.URL, "http://")+"/token", "ya29.spike-token")

	llm := &callOnceLLM{tool: "http_read", args: `{"url":"` + api.URL + `/v1/messages"}`}
	h := workspace.Host{LLM: llm, Master: master, Approver: &alwaysYes{}, Log: slog.New(slog.DiscardHandler)}
	w, err := workspace.Open(h, "test", dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(w.Close)

	id := chat.NewID()
	w.Chats().Submit(id, "read the API")
	var out string
	if !eventually(func() bool {
		msgs, _ := w.Chats().Transcript(id)
		if len(msgs) < 2 {
			return false
		}
		out = msgs[len(msgs)-1].Content
		return true
	}) {
		t.Fatal("the turn never finished")
	}
	if gotAuth != "Bearer ya29.spike-token" {
		t.Errorf("the API saw Authorization %q, want the bound credential", gotAuth)
	}
	if strings.Contains(out, "ya29.spike-token") {
		t.Errorf("the token came back to the model in %q; the injector must stay host-side", out)
	}
}

// TestAnUnconfiguredSkillBindsNothing: a skill whose address is not set yet has no host to bind a
// credential to, so nothing is registered — and the workspace still opens, because an extension
// somebody has not finished setting up is a state, not a failure.
func TestAnUnconfiguredSkillBindsNothing(t *testing.T) {
	dir := t.TempDir()
	writeSkillExtension(t, dir, "house", "") // manifest, no config.json

	master := testMaster(t)
	h := workspace.Host{LLM: &callOnceLLM{}, Master: master, Log: slog.New(slog.DiscardHandler)}
	w, err := workspace.Open(h, "test", dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(w.Close)

	// The skill is still in the catalog — hiding it would have the assistant deny it can do the thing
	// instead of naming the one command that finishes the setup.
	if !slices.Contains(w.Inventory().Skills, "house") {
		t.Fatalf("an unconfigured skill vanished from the catalog: %v", w.Inventory().Skills)
	}
}

// writeSkillExtension installs a skill that declares a credential bound to a configured address. An
// empty baseURL writes no config.json, which is an installed-but-unconfigured extension.
func writeSkillExtension(t *testing.T, wsDir, name, baseURL string) {
	t.Helper()
	dir := filepath.Join(wsDir, "extensions", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: Talk to the household server.\n---\nCall {{config.base_url}}/v1/messages.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := `{"config":[{"name":"base_url","type":"url"}],
		"credentials":[{"name":"token","host":"{{config.base_url}}","header":"Authorization","prefix":"Bearer ","audience":"model"}]}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if baseURL == "" {
		return
	}
	writeFileJSON(t, filepath.Join(dir, "config.json"), map[string]string{"base_url": baseURL})
}

// seedShard stores a credential value in an extension's own encrypted shard — what `nocturn secret
// set` writes, and the only place a value ever lives.
func seedShard(t *testing.T, master *secret.Master, wsDir, wsName, relPath, key, value string) {
	t.Helper()
	sv, err := secret.OpenShard(master, wsDir, wsName, relPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := sv.Set(key, []byte(value)); err != nil {
		t.Fatal(err)
	}
}

// testMaster derives a master key over a scratch salt — the same derivation the daemon does, at the
// lowest work factor so a test is not a key-stretching benchmark.
func testMaster(t *testing.T) *secret.Master {
	t.Helper()
	m, err := secret.DeriveMaster("spike-passphrase", []byte("0123456789abcdef"), secret.WithWorkFactor(10))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func writeFileJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// callOnceLLM calls one tool with fixed arguments, then answers with whatever the tool returned.
type callOnceLLM struct{ tool, args string }

func (l *callOnceLLM) Next(_ context.Context, conv []agentkit.Message, _ []agentkit.ToolSpec) (agentkit.Step, error) {
	if n := len(conv); n > 0 && conv[n-1].Role == agentkit.RoleTool {
		return agentkit.Step{Answer: conv[n-1].Content}, nil
	}
	return agentkit.Step{ToolCalls: []agentkit.ToolCall{{ID: "c1", Tool: l.tool, Args: l.args}}}, nil
}

// alwaysYes stands in for the human at the phone, approving without remembering.
type alwaysYes struct{}

var _ gate.Approver = (*alwaysYes)(nil)

func (alwaysYes) Ask(context.Context, gate.Action, gate.Recall, []gate.Grant) (bool, gate.Grant, gate.Recall, error) {
	return true, gate.Grant{}, gate.RecallNever, nil
}
