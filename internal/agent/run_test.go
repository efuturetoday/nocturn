package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/filecap"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// readWriteGuard is the workspace base policy the app ships: a file READ runs
// silently (Allow), a file WRITE asks out of band (Ask). engine drives the HITL.
func readWriteGuard(engine *hitl.Engine, epochs *capability.EpochRegistry) *gateway.Guard {
	return &gateway.Guard{
		Policy: capability.Policy{Rules: []capability.Rule{
			{Family: "file", TargetGlob: capability.Wildcard, Writes: capability.MatchRead, Effect: capability.Allow, Epoch: capability.Permanent},
			{Family: "file", TargetGlob: capability.Wildcard, Writes: capability.MatchWrite, Effect: capability.Ask, Epoch: capability.Permanent},
		}},
		Approvals: engine,
		Epochs:    epochs,
		TTL:       time.Second,
	}
}

// The whole agent path, headless and end to end: agent.RunTask drives a filtered
// brain over the real filecap tools through the real Guard. It proves the security
// model on an agent run — a READ runs silently (the human is never asked, and its
// content is fed back to the model), while a WRITE asks out of band exactly once.
func TestRunTask_ReadsSilent_WritesAsk_E2E(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes", "in.txt"), []byte("hello-source"), 0o600); err != nil {
		t.Fatal(err)
	}

	notifier := &autoNotifier{want: hitl.Approved} // approve the one write, "just this once"
	engine := hitl.NewEngine([]byte("test-key"), notifier)
	notifier.resolve = engine.Resolve
	epochs := capability.NewEpochRegistry()
	guard := readWriteGuard(engine, epochs)

	files := filecap.New(guard, root)
	reg := tool.NewRegistry(files.Tools())

	// The model reads a file, then writes one, then answers.
	model := &scriptedModel{steps: []brain.Step{
		{ToolCalls: []brain.ToolCall{{Tool: "file.read", Args: `{"path":"notes/in.txt"}`}}},
		{ToolCalls: []brain.ToolCall{{Tool: "file.write", Args: `{"path":"notes/out.txt","content":"summary"}`}}},
		{Answer: "done"},
	}}
	b := &brain.Brain{Model: model, Registry: reg}

	def := agent.Definition{Name: "triage", Tools: []string{"file"}, Instructions: "Summarize.", When: "manual"}
	ans, err := agent.RunTask(context.Background(), b, epochs, nil, def, "do it")
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	if ans != "done" {
		t.Fatalf("answer = %q, want \"done\"", ans)
	}
	// The read was silent AND succeeded: the human was asked only for the write,
	// and the read's content was fed back to the model on the next turn.
	if notifier.calls != 1 {
		t.Fatalf("human asked %d times, want 1 (write only — the read must be silent)", notifier.calls)
	}
	if len(model.convs) < 2 || !convContains(model.convs[1], "hello-source") {
		t.Fatal("the read result was not fed back to the model — read did not run")
	}
	// The write actually happened (behind the one approval).
	got, err := os.ReadFile(filepath.Join(root, "notes", "out.txt"))
	if err != nil || string(got) != "summary" {
		t.Fatalf("out.txt = %q err=%v, want \"summary\"", got, err)
	}
}

// The agent's tools list is an Action-Cage, not a hint: a tool the agent did not
// declare is structurally UNREACHABLE, even if the model names it. The effect is
// never attempted and the human is never asked — it fails as an unknown tool.
func TestRunTask_UndeclaredToolIsUnreachable_E2E(t *testing.T) {
	root := t.TempDir()
	notifier := &autoNotifier{want: hitl.Approved}
	engine := hitl.NewEngine([]byte("test-key"), notifier)
	notifier.resolve = engine.Resolve
	epochs := capability.NewEpochRegistry()
	guard := readWriteGuard(engine, epochs)

	// The registry HAS file tools plus a would-be dangerous one; the agent declares
	// only "file", so the dangerous tool is filtered out of its brain.
	var dangerCalled bool
	danger := tool.Tool{
		Spec:   tool.Spec{Name: "shell.exec"},
		Invoke: func(context.Context, string) (string, error) { dangerCalled = true; return "pwned", nil },
	}
	reg := tool.NewRegistry(append(filecap.New(guard, root).Tools(), danger))

	model := &scriptedModel{steps: []brain.Step{
		{ToolCalls: []brain.ToolCall{{Tool: "shell.exec", Args: `{"cmd":"rm -rf /"}`}}},
		{Answer: "blocked"},
	}}
	b := &brain.Brain{Model: model, Registry: reg}

	def := agent.Definition{Name: "reader", Tools: []string{"file"}, Instructions: "Read only.", When: "manual"}
	ans, err := agent.RunTask(context.Background(), b, epochs, nil, def, "try to escape")
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	if dangerCalled {
		t.Fatal("escape: shell.exec ran — an undeclared tool must be unreachable, not merely hidden")
	}
	if notifier.calls != 0 {
		t.Fatalf("human asked %d times for an undeclared tool, want 0", notifier.calls)
	}
	if ans != "blocked" {
		t.Fatalf("answer = %q, want \"blocked\"", ans)
	}
}
