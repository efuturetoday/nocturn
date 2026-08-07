package transcript_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/tui/transcript"
)

// t0 is the fixed clock every test folds against — the fold takes `now` as an argument precisely so
// no test needs a real one.
var t0 = time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

// fold applies a sequence to a view, the way the UI's watcher does one event at a time.
func fold(v transcript.View, evs ...agentkit.Event) transcript.View {
	for _, ev := range evs {
		v = transcript.Apply(v, ev, t0)
	}
	return v
}

func TestApplyOpensAndFillsTheAssistantBlock(t *testing.T) {
	v := fold(transcript.PushUser(transcript.View{}, "q"),
		agentkit.TurnStart{},
		agentkit.Thinking{Text: "hmm"},
		agentkit.Token{Text: "he"},
		agentkit.Token{Text: "llo"},
		agentkit.TurnEnd{Tokens: agentkit.TokenCount{Total: 42}},
	)

	if len(v.Blocks) != 2 {
		t.Fatalf("Blocks = %d, want 2", len(v.Blocks))
	}
	if got := v.Blocks[0]; got.Role != transcript.User || got.Text != "q" {
		t.Errorf("Blocks[0] = %+v, want the user's message", got)
	}
	a := v.Blocks[1]
	if a.Role != transcript.Assistant || a.Text != "hello" || a.Think != "hmm" {
		t.Errorf("Blocks[1] = %+v, want the assembled answer", a)
	}
	if a.Pending || v.Running {
		t.Errorf("Pending = %v, Running = %v, want both false after TurnEnd", a.Pending, v.Running)
	}
	if a.Tokens != 42 {
		t.Errorf("Tokens = %d, want 42", a.Tokens)
	}
}

func TestTurnStartIsIdempotent(t *testing.T) {
	v := fold(transcript.View{}, agentkit.TurnStart{}, agentkit.TurnStart{})
	if len(v.Blocks) != 1 {
		t.Fatalf("Blocks = %d, want 1 — a second TurnStart must not open a second block", len(v.Blocks))
	}
}

func TestEventsWithoutAnAssistantBlockAreDropped(t *testing.T) {
	// A token arriving before its TurnStart has nowhere to go; it must not land on the user's block.
	v := fold(transcript.PushUser(transcript.View{}, "q"), agentkit.Token{Text: "stray"})
	if len(v.Blocks) != 1 || v.Blocks[0].Text != "q" {
		t.Fatalf("Blocks = %+v, want the user block untouched", v.Blocks)
	}
}

func TestToolNesting(t *testing.T) {
	// A sub-agent call (id 1) whose own tool call (id 2) carries frame 1, and a third level.
	v := fold(transcript.View{},
		agentkit.TurnStart{},
		agentkit.ToolStart{ID: 1, Tool: "agent_research"},
		agentkit.ToolStart{Frame: 1, ID: 2, Tool: "http_read"},
		agentkit.ToolStart{Frame: 2, ID: 3, Tool: "dns_resolve"},
		agentkit.ToolStart{ID: 4, Tool: "time_now"},
	)

	tools := v.Blocks[0].Tools
	want := []struct {
		name  string
		depth int
	}{
		{"agent_research", 0},
		{"http_read", 1},
		{"dns_resolve", 2},
		{"time_now", 0},
	}
	if len(tools) != len(want) {
		t.Fatalf("Tools = %d, want %d", len(tools), len(want))
	}
	for i, w := range want {
		if tools[i].Name != w.name || tools[i].Depth != w.depth {
			t.Errorf("Tools[%d] = %s@%d, want %s@%d", i, tools[i].Name, tools[i].Depth, w.name, w.depth)
		}
	}
}

func TestToolEndUpdatesItsStartInPlace(t *testing.T) {
	v := fold(transcript.View{},
		agentkit.TurnStart{},
		agentkit.ToolStart{ID: 1, Tool: "http_read", Args: `{"url":"x"}`},
		agentkit.ToolEnd{ID: 1, Tool: "http_read", Args: `{"url":"x"}`, Result: "ok", Duration: 7 * time.Millisecond},
	)

	tools := v.Blocks[0].Tools
	if len(tools) != 1 {
		t.Fatalf("Tools = %d, want the end to update the start, not append", len(tools))
	}
	got := tools[0]
	if got.Running {
		t.Error("Running = true, want false after ToolEnd")
	}
	if got.Result != "ok" || got.Duration != 7*time.Millisecond {
		t.Errorf("Tool = %+v, want the result and duration filled in", got)
	}
	if !got.Started.Equal(t0) {
		t.Errorf("Started = %v, want the start's timestamp kept (%v)", got.Started, t0)
	}
}

func TestNestedTokensFeedTheSubAgentTail(t *testing.T) {
	v := fold(transcript.View{},
		agentkit.TurnStart{},
		agentkit.ToolStart{ID: 1, Tool: "agent_research"},
		agentkit.Token{Frame: 1, Text: "sub "},
		agentkit.Thinking{Frame: 1, Text: "thought"},
		agentkit.Token{Text: "top"},
	)

	block := v.Blocks[0]
	if block.Text != "top" {
		t.Errorf("Text = %q, want only the top-level token — nested text must not enter the answer", block.Text)
	}
	if got := block.Tools[0].Stream; got != "sub thought" {
		t.Errorf("Stream = %q, want the nested tokens", got)
	}
}

func TestSubAgentTailIsCapped(t *testing.T) {
	evs := []agentkit.Event{agentkit.TurnStart{}, agentkit.ToolStart{ID: 1, Tool: "agent_research"}}
	for range 400 {
		evs = append(evs, agentkit.Token{Frame: 1, Text: strings.Repeat("ä", 10)})
	}
	v := fold(transcript.View{}, evs...)

	tail := v.Blocks[0].Tools[0].Stream
	if len(tail) > 2048 {
		t.Errorf("len(Stream) = %d, want it trimmed to at most 2048 bytes", len(tail))
	}
	if !strings.HasSuffix(tail, "ä") || strings.Contains(tail, "�") {
		t.Errorf("Stream = %q…, want the trim to land on a rune boundary", tail[:min(20, len(tail))])
	}
}

func TestNestedTurnEndDoesNotEndTheView(t *testing.T) {
	// Running is set by the caller on submit; a sub-agent finishing must not clear it.
	running := fold(transcript.View{Running: true},
		agentkit.TurnStart{},
		agentkit.ToolStart{ID: 1, Tool: "agent_research"},
	)

	v := fold(running, agentkit.TurnEnd{Frame: 1})

	if !v.Running {
		t.Error("Running = false, want the turn still live after a nested TurnEnd")
	}
	if !v.Blocks[0].Pending {
		t.Error("Pending = false, want the top-level block still open after a nested TurnEnd")
	}
}

func TestStopReasons(t *testing.T) {
	tests := map[string]struct {
		err           error
		wantErr       string
		wantCancelled bool
	}{
		"clean":     {nil, "", false},
		"max steps": {agentkit.ErrMaxSteps, agentkit.ErrMaxSteps.Error(), false},
		"cancelled": {context.Canceled, context.Canceled.Error(), true},
		"wrapped":   {errors.Join(errors.New("outer"), context.Canceled), "", true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			v := fold(transcript.View{}, agentkit.TurnStart{}, agentkit.TurnEnd{Err: tt.err})
			got := v.Blocks[0]
			if tt.wantErr != "" && got.Err != tt.wantErr {
				t.Errorf("Err = %q, want %q", got.Err, tt.wantErr)
			}
			if got.Cancelled != tt.wantCancelled {
				t.Errorf("Cancelled = %v, want %v", got.Cancelled, tt.wantCancelled)
			}
		})
	}
}

func TestApplyDoesNotMutateTheInputView(t *testing.T) {
	before := fold(transcript.View{}, agentkit.TurnStart{}, agentkit.Token{Text: "a"},
		agentkit.ToolStart{ID: 1, Tool: "http_read"})

	_ = fold(before, agentkit.Token{Text: "b"}, agentkit.ToolEnd{ID: 1, Tool: "http_read", Result: "ok"})

	if got := before.Blocks[0].Text; got != "a" {
		t.Errorf("Text = %q, want %q — Apply must not write through the view it was handed", got, "a")
	}
	if before.Blocks[0].Tools[0].Result != "" {
		t.Error("the earlier view's tool was mutated by a later fold")
	}
}

func TestSeedMergesAssistantMessagesOfOneTurn(t *testing.T) {
	msgs := []agentkit.Message{
		{Role: agentkit.RoleSystem, Content: "ignored"},
		{Role: agentkit.RoleUser, Content: "q"},
		{Role: agentkit.RoleAssistant, ToolCalls: []agentkit.ToolCall{{ID: "c1", Tool: "http_read"}}},
		{Role: agentkit.RoleTool, Content: "ok", ToolCallID: "c1"},
		{Role: agentkit.RoleAssistant, Content: "answer"},
		{Role: agentkit.RoleUser, Content: "q2"},
		{Role: agentkit.RoleAssistant, Content: "second"},
	}
	forest := [][]chat.ToolNode{
		{{ID: 1, Tool: "http_read", DurationMs: 7}},
		nil,
	}

	v := transcript.Seed(msgs, forest, chat.InflightTurn{}, t0)

	if len(v.Blocks) != 4 {
		t.Fatalf("Blocks = %d, want 4 (user, assistant, user, assistant)", len(v.Blocks))
	}
	if got := v.Blocks[1]; got.Text != "answer" || len(got.Tools) != 1 {
		t.Errorf("Blocks[1] = %+v, want one merged assistant block carrying the turn's forest", got)
	}
	if got := v.Blocks[3]; got.Text != "second" || len(got.Tools) != 0 {
		t.Errorf("Blocks[3] = %+v, want the second turn with no tools", got)
	}
	if v.Running {
		t.Error("Running = true, want false with no in-flight turn")
	}
}

func TestSeedRestoresForestDepth(t *testing.T) {
	msgs := []agentkit.Message{
		{Role: agentkit.RoleUser, Content: "q"},
		{Role: agentkit.RoleAssistant, Content: "a"},
	}
	forest := [][]chat.ToolNode{{
		{ID: 1, Parent: 0, Tool: "agent_research"},
		{ID: 2, Parent: 1, Tool: "http_read"},
		{ID: 3, Parent: 2, Tool: "dns_resolve"},
		{ID: 4, Parent: 9, Tool: "orphan"}, // parent missing: falls back to top level
		{ID: 5, Parent: 5, Tool: "cycle"},  // self-parent: the guard must terminate
	}}

	tools := transcript.Seed(msgs, forest, chat.InflightTurn{}, t0).Blocks[1].Tools

	want := []int{0, 1, 2, 0, 1}
	for i, w := range want {
		if tools[i].Depth != w {
			t.Errorf("Tools[%d] (%s) depth = %d, want %d", i, tools[i].Name, tools[i].Depth, w)
		}
	}
}

func TestSeedReplaysTheInflightTurn(t *testing.T) {
	msgs := []agentkit.Message{
		{Role: agentkit.RoleUser, Content: "old"},
		{Role: agentkit.RoleAssistant, Content: "done"},
	}
	inf := chat.InflightTurn{
		Running: true,
		Input:   "new",
		Events: []agentkit.Event{
			agentkit.TurnStart{},
			agentkit.ToolStart{ID: 1, Tool: "http_read"},
			agentkit.Token{Text: "partial"},
		},
	}

	v := transcript.Seed(msgs, nil, inf, t0)

	if !v.Running {
		t.Error("Running = false, want true — the chat was reopened mid-turn")
	}
	if len(v.Blocks) != 4 {
		t.Fatalf("Blocks = %d, want the finished turn plus the replayed user and assistant blocks", len(v.Blocks))
	}
	live := v.Blocks[3]
	if !live.Pending || live.Text != "partial" || len(live.Tools) != 1 || !live.Tools[0].Running {
		t.Errorf("Blocks[3] = %+v, want the streaming block with its open call", live)
	}
}

func TestSeedOpensTheBlockBeforeTheFirstEvent(t *testing.T) {
	// The submit-to-TurnStart window: input recorded, nothing streamed yet.
	v := transcript.Seed(nil, nil, chat.InflightTurn{Running: true, Input: "q"}, t0)

	if len(v.Blocks) != 2 || !v.Blocks[1].Pending {
		t.Fatalf("Blocks = %+v, want the user block plus an open assistant block", v.Blocks)
	}
}

// TestConvergence is the warranty: a turn folded live must render the same as the snapshot the
// daemon persists for it. Without this the two paths drift silently — the failure mode the whole
// design is shaped to prevent.
func TestConvergence(t *testing.T) {
	live := fold(transcript.PushUser(transcript.View{}, "q"),
		agentkit.TurnStart{},
		agentkit.ToolStart{ID: 1, Tool: "agent_research", Args: `{"task":"x"}`},
		agentkit.ToolStart{Frame: 1, ID: 2, Tool: "http_read", Args: `{"url":"y"}`},
		agentkit.Token{Frame: 1, Text: "sub-agent chatter"},
		agentkit.ToolEnd{Frame: 1, ID: 2, Tool: "http_read", Args: `{"url":"y"}`, Result: "page", Duration: 3 * time.Millisecond},
		agentkit.ToolEnd{ID: 1, Tool: "agent_research", Args: `{"task":"x"}`, Result: "summary", Duration: 11 * time.Millisecond},
		agentkit.Token{Text: "answer"},
		agentkit.TurnEnd{Tokens: agentkit.TokenCount{Total: 2}},
	)

	// The same turn as the daemon persists it: the agentkit transcript plus the turn's forest group,
	// built the way internal/chat's forest does (start order, parent = enclosing frame).
	msgs := []agentkit.Message{
		{Role: agentkit.RoleUser, Content: "q"},
		{Role: agentkit.RoleAssistant, ToolCalls: []agentkit.ToolCall{{ID: "c1", Tool: "agent_research", Args: `{"task":"x"}`}}},
		{Role: agentkit.RoleTool, Content: "summary", ToolCallID: "c1"},
		{Role: agentkit.RoleAssistant, Content: "answer"},
	}
	forest := [][]chat.ToolNode{{
		{ID: 1, Parent: 0, Tool: "agent_research", Args: `{"task":"x"}`, Result: "summary", DurationMs: 11},
		{ID: 2, Parent: 1, Tool: "http_read", Args: `{"url":"y"}`, Result: "page", DurationMs: 3},
	}}
	seeded := transcript.Seed(msgs, forest, chat.InflightTurn{}, t0)

	if !reflect.DeepEqual(normalize(live), normalize(seeded)) {
		t.Errorf("live fold and snapshot seed differ:\n live = %+v\n seed = %+v", normalize(live), normalize(seeded))
	}
	if live.Running != seeded.Running {
		t.Errorf("Running: live = %v, seed = %v", live.Running, seeded.Running)
	}
}

// normalize drops what only one path can know — the live timer's Started, the sub-agent tail, and
// the token count, none of which is persisted. Everything that renders must match.
func normalize(v transcript.View) []map[string]any {
	out := make([]map[string]any, 0, len(v.Blocks))
	for _, b := range v.Blocks {
		tools := make([]map[string]any, 0, len(b.Tools))
		for _, t := range b.Tools {
			tools = append(tools, map[string]any{
				"name": t.Name, "args": t.Args, "result": t.Result, "err": t.Err,
				"depth": t.Depth, "parent": t.Parent, "duration": t.Duration, "running": t.Running,
			})
		}
		out = append(out, map[string]any{
			"role": b.Role, "text": b.Text, "err": b.Err, "pending": b.Pending, "tools": tools,
		})
	}
	return out
}
