package agent_test

import (
	"context"
	"testing"

	"github.com/efuturetoday/nocturn/internal/activity"
	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// An ATTACHED child run inherits the parent's activity sink from ctx: the child
// agent's tool calls emit onto the same stream, so they nest into the parent chat.
// A DETACHED run (bare ctx, no sink) is silent — the same shared Brain/Registry, the
// mute is simply the absence of a sink. This is the whole attach/detach model.
func TestRun_AttachedInheritsActivitySink_DetachedSilent(t *testing.T) {
	// newDeps builds a fresh stateless Brain + the full tool registry for one run.
	newDeps := func() agent.Deps {
		reg := tool.NewRegistry().AddMany([]tool.Tool{{
			Spec:   tool.Spec{Name: "probe"},
			Invoke: func(context.Context, string) (string, error) { return "ok", nil },
		}}...)

		model := &scriptedModel{steps: []brain.Step{
			{ToolCalls: []brain.ToolCall{{Tool: "probe"}}},
			{Answer: "done"},
		}}
		return agent.Deps{Brain: brain.New(model), Tools: reg, Guard: &gateway.Guard{}}
	}
	def := agent.Agent{Name: "child", Tools: []string{"probe"}, Instructions: "go", When: "manual"}

	// Attached: a sink on ctx captures the child's tool events.
	var events []activity.ToolEvent
	attached := activity.WithSink(context.Background(), func(e activity.Event) {
		if te, ok := e.(activity.ToolEvent); ok {
			events = append(events, te)
		}
	})
	if _, err := agent.Run(attached, newDeps(), def, "t"); err != nil {
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
	if _, err := agent.Run(detached, newDeps(), def, "t"); err != nil {
		t.Fatalf("detached run: %v", err)
	}
}
