package agentkit_test

// Shared test fakes and helpers for the external (agentkit_test) suite. Built ONCE here and
// referenced from every *_test.go file in this package.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
)

// --- LLM fake ---

// stepLLM replays pre-scripted steps in order; once exhausted it repeats the LAST step (so a single
// tool-call step drives an unbounded loop). It records every Next call for assertions and can
// optionally block (gate) or announce entry (entered) to coordinate concurrency tests without sleeps.
type stepLLM struct {
	mu      sync.Mutex
	steps   []agentkit.Step
	err     error // if set, Next returns ({}, err) after recording the call
	calls   int
	convs   [][]agentkit.Message
	specs   [][]agentkit.ToolSpec
	entered chan struct{} // if non-nil, each Next signals entry before gating
	gate    chan struct{} // if non-nil, each Next waits for a token before returning
}

func (l *stepLLM) Next(ctx context.Context, conv []agentkit.Message, tools []agentkit.ToolSpec) (agentkit.Step, error) {
	if l.entered != nil {
		select {
		case l.entered <- struct{}{}:
		case <-ctx.Done():
			return agentkit.Step{}, ctx.Err()
		}
	}
	if l.gate != nil {
		select {
		case <-l.gate:
		case <-ctx.Done():
			return agentkit.Step{}, ctx.Err()
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	l.convs = append(l.convs, append([]agentkit.Message(nil), conv...))
	l.specs = append(l.specs, append([]agentkit.ToolSpec(nil), tools...))
	if l.err != nil {
		return agentkit.Step{}, l.err
	}
	if len(l.steps) == 0 {
		return agentkit.Step{}, nil
	}
	i := l.calls - 1
	if i >= len(l.steps) {
		i = len(l.steps) - 1
	}
	return l.steps[i], nil
}

func (l *stepLLM) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

func (l *stepLLM) convAt(i int) []agentkit.Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.convs[i]
}

func (l *stepLLM) specsAt(i int) []agentkit.ToolSpec {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.specs[i]
}

// --- Step / TokenCount builders ---

func tc(prompt, completion, total int) agentkit.TokenCount {
	return agentkit.TokenCount{Prompt: prompt, Completion: completion, Total: total}
}

func answerStep(text string) agentkit.Step { return agentkit.Step{Answer: text} }

func answerStepT(text string, tk agentkit.TokenCount) agentkit.Step {
	return agentkit.Step{Answer: text, Tokens: tk}
}

func callStep(id, tool, args string) agentkit.Step {
	return agentkit.Step{ToolCalls: []agentkit.ToolCall{{ID: id, Tool: tool, Args: args}}}
}

func callStepT(id, tool, args string, tk agentkit.TokenCount) agentkit.Step {
	return agentkit.Step{ToolCalls: []agentkit.ToolCall{{ID: id, Tool: tool, Args: args}}, Tokens: tk}
}

// --- tool builders ---

func newTool(t *testing.T, name string, fn agentkit.ToolFunc, opts ...agentkit.ToolOption) agentkit.Tool {
	t.Helper()
	tool, err := agentkit.NewTool(name, name+" tool", fn, opts...)
	if err != nil {
		t.Fatalf("NewTool(%q): %v", name, err)
	}
	return tool
}

func newSet(t *testing.T, tools ...agentkit.Tool) agentkit.ToolSet {
	t.Helper()
	set, err := agentkit.NewToolSet(tools...)
	if err != nil {
		t.Fatalf("NewToolSet: %v", err)
	}
	return set
}

// echoTool returns result on every call.
func echoTool(t *testing.T, name, result string) agentkit.Tool {
	t.Helper()
	return newTool(t, name, func(context.Context, string) (string, error) { return result, nil })
}

// --- Store fake ---

type fakeStore struct {
	mu       sync.Mutex
	saves    int
	last     []agentkit.Message
	loadMsgs []agentkit.Message
	loadErr  error
	saveErr  error
}

func (s *fakeStore) Save(_ string, msgs []agentkit.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	s.last = append([]agentkit.Message(nil), msgs...)
	return s.saveErr
}

func (s *fakeStore) Load(string) ([]agentkit.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return append([]agentkit.Message(nil), s.loadMsgs...), nil
}

func (s *fakeStore) List() ([]string, error) { return nil, nil }

func (s *fakeStore) saveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves
}

func (s *fakeStore) history() []agentkit.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]agentkit.Message(nil), s.last...)
}

// --- event sink capture ---

type captureSink struct {
	mu     sync.Mutex
	events []agentkit.Event
}

func (c *captureSink) fn() func(agentkit.Event) {
	return func(e agentkit.Event) {
		c.mu.Lock()
		c.events = append(c.events, e)
		c.mu.Unlock()
	}
}

func (c *captureSink) all() []agentkit.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]agentkit.Event(nil), c.events...)
}

// --- logger fake ---

type logLine struct{ level, msg string }

type memLogger struct {
	mu    sync.Mutex
	lines []logLine
}

func (m *memLogger) record(level, msg string) {
	m.mu.Lock()
	m.lines = append(m.lines, logLine{level, msg})
	m.mu.Unlock()
}

func (m *memLogger) Debug(msg string, _ ...any) { m.record("debug", msg) }
func (m *memLogger) Info(msg string, _ ...any)  { m.record("info", msg) }
func (m *memLogger) Warn(msg string, _ ...any)  { m.record("warn", msg) }
func (m *memLogger) Error(msg string, _ ...any) { m.record("error", msg) }

func (m *memLogger) With(...any) agentkit.Logger                 { return m }
func (m *memLogger) WithContext(context.Context) agentkit.Logger { return m }

func (m *memLogger) hasWarn(substr string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range m.lines {
		if l.level == "warn" && strings.Contains(l.msg, substr) {
			return true
		}
	}
	return false
}

// --- tokenizer spy ---

type spyTokenizer struct {
	mu    sync.Mutex
	calls int
	err   error
	per   int // tokens per call when err is nil and per>0; else rune count
}

func (s *spyTokenizer) Count(text string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return 0, s.err
	}
	if s.per > 0 {
		return s.per, nil
	}
	return len([]rune(text)), nil
}

func (s *spyTokenizer) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// --- Session helper: build + auto-close/drain to avoid leaked worker goroutines ---

func newSession(t *testing.T, llm agentkit.LLM, opts ...agentkit.Option) (*agentkit.Session, <-chan agentkit.Event) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := agentkit.NewSession(ctx, llm, opts...)
	sub := s.Subscribe()
	t.Cleanup(func() {
		cancel()
		for range sub { //nolint:revive // drain so the worker exits without blocking on a full buffer
		}
	})
	return s, sub
}

// nextTurnEnd reads sub until it yields a TurnEnd, returning the events consumed (inclusive). It
// leaves the channel open.
func nextTurnEnd(t *testing.T, sub <-chan agentkit.Event) (agentkit.TurnEnd, []agentkit.Event) {
	t.Helper()
	var got []agentkit.Event
	for e := range sub {
		got = append(got, e)
		if te, ok := e.(agentkit.TurnEnd); ok {
			return te, got
		}
	}
	t.Fatal("subscribe closed before TurnEnd")
	return agentkit.TurnEnd{}, got
}

// --- event filters ---

func lastTurnEnd(t *testing.T, events []agentkit.Event) agentkit.TurnEnd {
	t.Helper()
	for i := len(events) - 1; i >= 0; i-- {
		if te, ok := events[i].(agentkit.TurnEnd); ok {
			return te
		}
	}
	t.Fatalf("no TurnEnd among %d events", len(events))
	return agentkit.TurnEnd{}
}

func toolStartEvents(events []agentkit.Event) []agentkit.ToolStart {
	var out []agentkit.ToolStart
	for _, e := range events {
		if ts, ok := e.(agentkit.ToolStart); ok {
			out = append(out, ts)
		}
	}
	return out
}

func toolEndEvents(events []agentkit.Event) []agentkit.ToolEnd {
	var out []agentkit.ToolEnd
	for _, e := range events {
		if te, ok := e.(agentkit.ToolEnd); ok {
			out = append(out, te)
		}
	}
	return out
}
