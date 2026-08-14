package workspace_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/workspace"
)

// A workspace-level binding is what makes a REST API reachable by the MODEL rather than by a plugin:
// bindings.json registers under owner "", and the net tools consult the injector by destination host.
// internal/tools proves the injection itself; what this proves is the seam above it — that a
// credential seeded into the vault, named in bindings.json, and never mentioned in a prompt arrives
// as a header on the model's own http_read.
//
// It is the whole basis for reaching Gmail, Calendar, Drive or Graph from a skill: the skill writes
// URLs, the host attaches the account.
func TestAWorkspaceBindingReachesTheModelsHTTPTool(t *testing.T) {
	var gotAuth string
	var once sync.Once
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { gotAuth = r.Header.Get("Authorization") })
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[]}`))
	}))
	defer api.Close()

	dir := t.TempDir()
	host := strings.TrimPrefix(api.URL, "http://")
	writeBindings(t, filepath.Join(dir, "bindings.json"), []map[string]string{{
		"secret": "acct_token",
		"host":   host,
		"header": "Authorization",
		"prefix": "Bearer ",
	}})

	// The vault has to be unlocked for any of this to exist: a locked workspace has no injector at
	// all, which is the fail-closed side of the same design.
	t.Setenv("NOCTURN_SECRET_ACCT_TOKEN", "ya29.spike-token")
	master := testMaster(t)

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

func writeBindings(t *testing.T, path string, v any) {
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

func (alwaysYes) Ask(context.Context, gate.Action, []gate.Grant) (bool, gate.Grant, gate.Recall, error) {
	return true, gate.Grant{}, gate.RecallNever, nil
}
