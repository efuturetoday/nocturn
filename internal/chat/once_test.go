package chat_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/activity"
	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/filecap"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// agentCharter compiles an agent declaration into a Charter the way the workspace
// does (filtered tools, Instructions as system) — the test-side stand-in for
// workspace.AgentCharter, which needs a real workspace directory.
func agentCharter(def agent.Agent, reg *tool.Registry) chat.Charter {
	return chat.Charter{
		Tools:  reg.Select(def.Matches),
		System: def.Instructions,
		Authority: gateway.Authority{
			Policy: def.Policy,
			Cage:   def.Cage,
		},
		Budget: def.Budget,
	}
}

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

// The whole agent path, headless and end to end: chat.Once drives a filtered
// brain over the real filecap tools through the real Guard. It proves the security
// model on an agent run — a READ runs silently (the human is never asked, and its
// content is fed back to the model), while a WRITE asks out of band exactly once.
func TestOnce_ReadsSilent_WritesAsk_E2E(t *testing.T) {
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

	def := agent.Agent{Name: "triage", Tools: []string{"file"}, Instructions: "Summarize.", When: "manual"}
	answer, err := chat.Once(context.Background(), brain.New(model), guard, agentCharter(def, reg), "do it")
	if err != nil {
		t.Fatalf("Once: %v", err)
	}

	if answer != "done" {
		t.Fatalf("answer = %q, want \"done\"", answer)
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
func TestOnce_UndeclaredToolIsUnreachable_E2E(t *testing.T) {
	root := t.TempDir()
	notifier := &autoNotifier{want: hitl.Approved}
	engine := hitl.NewEngine([]byte("test-key"), notifier)
	notifier.resolve = engine.Resolve
	guard := readWriteGuard(engine)

	// The registry HAS file tools plus a would-be dangerous one; the agent declares
	// only "file", so the dangerous tool is filtered out of its charter.
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

	def := agent.Agent{Name: "reader", Tools: []string{"file"}, Instructions: "Read only.", When: "manual"}
	answer, err := chat.Once(context.Background(), brain.New(model), guard, agentCharter(def, reg), "try to escape")
	if err != nil {
		t.Fatalf("Once: %v", err)
	}

	if dangerCalled {
		t.Fatal("escape: shell.exec ran — an undeclared tool must be unreachable, not merely hidden")
	}
	if notifier.calls != 0 {
		t.Fatalf("human asked %d times for an undeclared tool, want 0", notifier.calls)
	}
	if answer != "blocked" {
		t.Fatalf("answer = %q, want \"blocked\"", answer)
	}
}

// A child agent's Instructions ARE its system prompt: they seed the conversation's
// leading role=system message, and the task rides in as a SEPARATE, raw role=user
// message — not glued onto the instructions. This is the subagent's standing identity
// vs. its transient task, kept in distinct roles.
func TestOnce_InstructionsSeedSystem_TaskIsRawUser(t *testing.T) {
	model := &scriptedModel{steps: []brain.Step{{Answer: "ok"}}}
	def := agent.Agent{Name: "worker", Instructions: "You are a focused worker.", When: "manual"}

	if _, err := chat.Once(context.Background(), brain.New(model), &gateway.Guard{}, agentCharter(def, tool.NewRegistry()), "do the thing"); err != nil {
		t.Fatalf("Once: %v", err)
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
// conversation starts straight at the user's task. An empty System is a no-op.
func TestOnce_NoInstructions_NoSystemTurn(t *testing.T) {
	model := &scriptedModel{steps: []brain.Step{{Answer: "ok"}}}
	def := agent.Agent{Name: "bare", When: "manual"}

	if _, err := chat.Once(context.Background(), brain.New(model), &gateway.Guard{}, agentCharter(def, tool.NewRegistry()), "hi"); err != nil {
		t.Fatalf("Once: %v", err)
	}

	conv := model.convs[0]
	if len(conv) != 1 || conv[0].Role != "user" {
		t.Fatalf("conversation = %+v, want a single role=user turn (no system seed)", conv)
	}
}

// An ATTACHED one-shot run inherits the parent's activity sink from ctx: the child
// agent's tool calls emit onto the same stream, so they nest into the parent chat.
// A DETACHED run (bare ctx, no sink) is silent — the same shared Brain/Registry, the
// mute is simply the absence of a sink. This is the whole attach/detach model.
func TestOnce_AttachedInheritsActivitySink_DetachedSilent(t *testing.T) {
	// newParts builds a fresh stateless Brain + the full tool registry for one run.
	newParts := func() (*brain.Brain, *tool.Registry) {
		reg := tool.NewRegistry().AddMany([]tool.Tool{{
			Spec:   tool.Spec{Name: "probe"},
			Invoke: func(context.Context, string) (string, error) { return "ok", nil },
		}}...)
		model := &scriptedModel{steps: []brain.Step{
			{ToolCalls: []brain.ToolCall{{Tool: "probe"}}},
			{Answer: "done"},
		}}
		return brain.New(model), reg
	}
	def := agent.Agent{Name: "child", Tools: []string{"probe"}, Instructions: "go", When: "manual"}

	// Attached: a sink on ctx captures the child's tool events.
	var events []activity.ToolEvent
	attached := activity.WithSink(context.Background(), func(e activity.Event) {
		if te, ok := e.(activity.ToolEvent); ok {
			events = append(events, te)
		}
	})
	b, reg := newParts()
	if _, err := chat.Once(attached, b, &gateway.Guard{}, agentCharter(def, reg), "t"); err != nil {
		t.Fatalf("attached run: %v", err)
	}
	if len(events) != 2 || events[0].Tool != "probe" || events[0].Phase != activity.Start {
		t.Fatalf("attached child did not emit its probe start/end onto the inherited sink: %+v", events)
	}

	// Detached: a bare ctx carries no sink → the same run emits nothing (silent), yet
	// completes identically. The mute is the absence of a sink, not a muted registry.
	detached := context.Background()
	if s := activity.SinkFrom(detached); s != nil {
		t.Fatal("bare ctx unexpectedly carries a sink")
	}
	b, reg = newParts()
	if _, err := chat.Once(detached, b, &gateway.Guard{}, agentCharter(def, reg), "t"); err != nil {
		t.Fatalf("detached run: %v", err)
	}
}
