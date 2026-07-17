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
func readWriteGuard(engine *hitl.Engine) *gateway.Guard {
	return &gateway.Guard{
		Policy: capability.Policy{Rules: []capability.Rule{
			{Family: "file", TargetGlob: capability.Wildcard, Writes: capability.MatchRead, Effect: capability.Allow, Epoch: capability.Permanent},
			{Family: "file", TargetGlob: capability.Wildcard, Writes: capability.MatchWrite, Effect: capability.Ask, Epoch: capability.Permanent},
		}},
		Approvals: engine,
		TTL:       time.Second,
	}
}

// The whole agent path, headless and end to end: agent.Run drives a filtered
// brain over the real filecap tools through the real Guard. It proves the security
// model on an agent run — a READ runs silently (the human is never asked, and its
// content is fed back to the model), while a WRITE asks out of band exactly once.
func TestRun_ReadsSilent_WritesAsk_E2E(t *testing.T) {
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
	guard := readWriteGuard(engine)

	files := filecap.New(guard, root)
	reg := tool.NewRegistry().AddMany(files.Tools()...)

	// The model reads a file, then writes one, then answers.
	model := &scriptedModel{steps: []brain.Step{
		{ToolCalls: []brain.ToolCall{{Tool: "file.read", Args: `{"path":"notes/in.txt"}`}}},
		{ToolCalls: []brain.ToolCall{{Tool: "file.write", Args: `{"path":"notes/out.txt","content":"summary"}`}}},
		{Answer: "done"},
	}}
	b := brain.New(model)

	def := agent.Agent{Name: "triage", Tools: []string{"file"}, Instructions: "Summarize.", When: "manual"}
	res, err := agent.Run(context.Background(), agent.Deps{Brain: b, Tools: reg, Guard: guard}, def, "do it")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Answer != "done" {
		t.Fatalf("answer = %q, want \"done\"", res.Answer)
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
func TestRun_UndeclaredToolIsUnreachable_E2E(t *testing.T) {
	root := t.TempDir()
	notifier := &autoNotifier{want: hitl.Approved}
	engine := hitl.NewEngine([]byte("test-key"), notifier)
	notifier.resolve = engine.Resolve
	guard := readWriteGuard(engine)

	// The registry HAS file tools plus a would-be dangerous one; the agent declares
	// only "file", so the dangerous tool is filtered out of its brain.
	var dangerCalled bool
	danger := tool.Tool{
		Spec:   tool.Spec{Name: "shell.exec"},
		Invoke: func(context.Context, string) (string, error) { dangerCalled = true; return "pwned", nil },
	}
	reg := tool.NewRegistry().AddMany(append(filecap.New(guard, root).Tools(), danger)...)

	model := &scriptedModel{steps: []brain.Step{
		{ToolCalls: []brain.ToolCall{{Tool: "shell.exec", Args: `{"cmd":"rm -rf /"}`}}},
		{Answer: "blocked"},
	}}
	b := brain.New(model)

	def := agent.Agent{Name: "reader", Tools: []string{"file"}, Instructions: "Read only.", When: "manual"}
	res, err := agent.Run(context.Background(), agent.Deps{Brain: b, Tools: reg, Guard: guard}, def, "try to escape")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if dangerCalled {
		t.Fatal("escape: shell.exec ran — an undeclared tool must be unreachable, not merely hidden")
	}
	if notifier.calls != 0 {
		t.Fatalf("human asked %d times for an undeclared tool, want 0", notifier.calls)
	}
	if res.Answer != "blocked" {
		t.Fatalf("answer = %q, want \"blocked\"", res.Answer)
	}
}

// A child agent's Instructions ARE its system prompt: they seed the conversation's
// leading role=system message, and the task rides in as a SEPARATE, raw role=user
// message — not glued onto the instructions. This is the subagent's standing identity
// vs. its transient task, kept in distinct roles.
func TestRun_InstructionsSeedSystem_TaskIsRawUser(t *testing.T) {
	model := &scriptedModel{steps: []brain.Step{{Answer: "ok"}}}
	reg := tool.NewRegistry()
	def := agent.Agent{Name: "worker", Instructions: "You are a focused worker.", When: "manual"}

	if _, err := agent.Run(context.Background(), agent.Deps{Brain: brain.New(model), Tools: reg, Guard: &gateway.Guard{}}, def, "do the thing"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	conv := model.convs[0]
	if len(conv) != 2 {
		t.Fatalf("conversation = %d messages, want 2 (system + user): %+v", len(conv), conv)
	}
	if conv[0].Role != "system" || conv[0].Content != "You are a focused worker." {
		t.Fatalf("first message = %+v, want role=system with the Instructions verbatim", conv[0])
	}
	if conv[1].Role != "user" || conv[1].Content != "do the thing" {
		t.Fatalf("second message = %+v, want role=user with the RAW task (no Instructions glued on)", conv[1])
	}
}

// With empty Instructions (the interactive root agent), no system turn is seeded — the
// conversation starts straight at the user's task. WithSystem("") is a no-op.
func TestRun_NoInstructions_NoSystemTurn(t *testing.T) {
	model := &scriptedModel{steps: []brain.Step{{Answer: "ok"}}}
	def := agent.Agent{Name: "bare", When: "manual"}

	if _, err := agent.Run(context.Background(), agent.Deps{Brain: brain.New(model), Tools: tool.NewRegistry(), Guard: &gateway.Guard{}}, def, "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	conv := model.convs[0]
	if len(conv) != 1 || conv[0].Role != "user" {
		t.Fatalf("conversation = %+v, want a single role=user turn (no system seed)", conv)
	}
}
