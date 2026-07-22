package chat_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/runtime"
	"github.com/efuturetoday/nocturn/app/chat"
)

// funcLLM drives a session from a per-call function: it gets ctx (so the fake can Emit streaming
// answer/reasoning events, exactly as a real adapter does) and the running conversation (so it can
// decide tool-call vs answer). This is the scripted/blocking variant the lifecycle tests need beyond
// the trivial fakeLLM (which only ever answers "ok").
type funcLLM struct {
	next func(ctx context.Context, conv []agentkit.Message) (agentkit.Step, error)
}

func (l funcLLM) Next(ctx context.Context, conv []agentkit.Message, _ []agentkit.ToolSpec) (agentkit.Step, error) {
	return l.next(ctx, conv)
}

// toolThenAnswer issues exactly one call to tool at the start of every turn, then answers once the
// tool's result comes back — a single, deterministic tool call per turn (keyed off the last message's
// role, so it works turn after turn without any external counter).
func toolThenAnswer(tool string) funcLLM {
	return funcLLM{next: func(_ context.Context, conv []agentkit.Message) (agentkit.Step, error) {
		if conv[len(conv)-1].Role == agentkit.RoleTool {
			return agentkit.Step{Answer: "done"}, nil
		}
		return agentkit.Step{ToolCalls: []agentkit.ToolCall{{ID: "c1", Tool: tool, Args: "{}"}}}, nil
	}}
}

// blockingLLM parks in Next until release is closed or the turn's ctx is cancelled — for tests that
// need a turn that is durably "running" without racing a fast completion.
func blockingLLM(release <-chan struct{}) funcLLM {
	return funcLLM{next: func(ctx context.Context, _ []agentkit.Message) (agentkit.Step, error) {
		select {
		case <-release:
			return agentkit.Step{Answer: "ok"}, nil
		case <-ctx.Done():
			return agentkit.Step{}, ctx.Err()
		}
	}}
}

// okTool is a tool that returns immediately — used to make a turn produce a (completed) forest group.
func okTool(t *testing.T, name string) agentkit.Tool {
	t.Helper()
	tool, err := agentkit.NewTool(name, "test tool", func(context.Context, string) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("okTool: %v", err)
	}
	return tool
}

// blockingTool blocks inside Call until release is closed or ctx is cancelled — so a turn can be held
// mid-tool (in-flight forest with a still-running node) and later released or cancelled.
func blockingTool(t *testing.T, name string, release <-chan struct{}) agentkit.Tool {
	t.Helper()
	tool, err := agentkit.NewTool(name, "blocking test tool", func(ctx context.Context, _ string) (string, error) {
		select {
		case <-release:
			return "released", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	if err != nil {
		t.Fatalf("blockingTool: %v", err)
	}
	return tool
}

// toolRuntime builds a Runtime whose sessions expose the given tools (ungated: no policy configured,
// so tools run directly and their ToolStart/ToolEnd events flow into the manager's forest).
func toolRuntime(t *testing.T, llm agentkit.LLM, tools ...agentkit.Tool) *runtime.Runtime {
	t.Helper()
	ts, err := agentkit.NewToolSet(tools...)
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	return runtime.New(llm, runtime.WithTools(ts))
}

// newManagerRT builds a Manager over rt and returns it together with its store, WITHOUT registering a
// cleanup — the caller owns CloseAll. That is what synctest bubbles need (every bubble goroutine, the
// reaper and every pump included, must exit inside the bubble, so CloseAll has to run before the test
// function returns, not in a t.Cleanup that fires afterwards).
func newManagerRT(t *testing.T, rt *runtime.Runtime) (*chat.Manager, *chat.Store) {
	t.Helper()
	store, err := chat.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return chat.NewManager(rt, store, slog.New(slog.DiscardHandler)), store
}

// saveCounter records the store's OnSave firings, so a test can assert a persist did (or did not)
// broadcast. Concurrency-safe: the store fires OnSave outside its lock, possibly from several turns.
type saveCounter struct {
	mu   sync.Mutex
	n    int
	last chat.Meta
}

func (c *saveCounter) fn(m chat.Meta) {
	c.mu.Lock()
	c.n++
	c.last = m
	c.mu.Unlock()
}

func (c *saveCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// zeroInflight reports whether fl is the empty read-model (no running turn). Inflight holds a slice,
// so it is not == comparable — check the fields.
func zeroInflight(fl chat.Inflight) bool {
	return !fl.Running && fl.Input == "" && fl.Answer == "" && fl.Thinking == "" && len(fl.Tools) == 0
}
