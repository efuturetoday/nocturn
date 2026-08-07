// Package transcript is the ONE pure fold of a chat's rendered state — no terminal, no clock, no
// I/O. Both entry points reduce into the SAME View by the SAME builders, so the snapshot path and
// the live-stream path can never drift:
//
//   - Seed — the initial state from a persisted transcript + per-turn tool forest + the running turn.
//   - Apply — one live event folded onto the state.
//
// It is the Go sibling of the mobile client's chat-model.ts and follows it deliberately, down to the
// merge rules; the convergence test (a live sequence against the snapshot of its end state) pins the
// two paths together the same way. Keeping it free of go-tui is what makes the terminal UI testable
// without a PTY, and what lets the renderer change without touching the fold.
package transcript

import (
	"context"
	"errors"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/chat"
)

// streamCap bounds a sub-agent's live text tail. The tail exists to show that a nested agent is
// working, not to reproduce its answer — that arrives as the tool's Result. Older text is dropped
// from the front.
const streamCap = 2048

// Role is who a block belongs to. Notice is the UI's own voice: a proactive notification, a slash
// command's answer, an error the fold did not come from the model.
type Role uint8

const (
	User Role = iota
	Assistant
	Notice
)

// Tool is one call rendered under the assistant block that issued it. Depth is the length of the
// parent chain, so a sub-agent's internal calls indent under the AgentTool call that spawned them
// rather than interleaving with the answer.
type Tool struct {
	ID, Parent uint64
	Depth      int
	Name       string
	Args       string
	Result     string
	Err        string
	Running    bool
	// Started seeds the live ticking timer WHILE running; on end Duration freezes it from the
	// tool's own wall-clock, which is more accurate than a UI clock and matches the reloaded
	// snapshot.
	Started  time.Time
	Duration time.Duration
	// Stream is the tail of a sub-agent's answer/reasoning text while its call runs. The mobile
	// client drops nested tokens; a terminal has the room to show them, so they land here instead
	// of in the top-level answer.
	Stream string
}

// Block is one bubble: a user message, an assistant turn (its text, reasoning and whole tool
// forest), or a notice.
type Block struct {
	Role      Role
	Text      string
	Think     string
	Tools     []Tool
	Err       string
	Cancelled bool // the turn ended on context.Canceled — a stop, not a failure
	Tokens    int
	Pending   bool
}

// View is the assembled render state of one chat. Running drives the composer and the cancel hint.
type View struct {
	Blocks  []Block
	Running bool
}

// Seed builds the initial view: the finished turns from the transcript and forest, then the running
// turn folded on top. The in-flight turn is not in the transcript yet — it arrives as its raw
// material (inf.Input plus inf.Events) which is replayed through the SAME Apply as the live stream,
// so reopening mid-turn and watching one live render identically. now seeds a still-open call's
// timer. A running turn always shows its assistant block even before its first event streams (the
// submit-to-TurnStart window), so the composer state matches.
func Seed(msgs []agentkit.Message, forest [][]chat.ToolNode, inf chat.InflightTurn, now time.Time) View {
	v := View{Blocks: snapshotBlocks(msgs, forest)}
	if !inf.Running {
		return v
	}
	if inf.Input != "" {
		v = PushUser(v, inf.Input)
	}
	for _, ev := range inf.Events {
		v = Apply(v, ev, now)
	}
	if last := lastBlock(v); last == nil || last.Role != Assistant || !last.Pending {
		v = openAssistant(v)
	}
	v.Running = true
	return v
}

// Apply folds one live event onto the view. Only frame-0 events drive the visible answer; nested
// frames feed the tool forest and the sub-agent tails. now seeds a starting call's timer — passed
// in so this stays pure. The caller routes by chat id before calling; that is transport, not view
// state.
func Apply(v View, ev agentkit.Event, now time.Time) View {
	switch e := ev.(type) {
	case agentkit.TurnStart:
		// The assistant block is opened HERE, not inferred from the first token — so a locally
		// sent turn and a backend-initiated one (a wake resume, another device) look the same.
		if e.Frame != 0 {
			return v
		}
		return openAssistant(v)

	case agentkit.Token:
		if e.Frame == 0 {
			return editAssistant(v, func(b *Block) { b.Text += e.Text })
		}
		return editTool(v, e.Frame, func(t *Tool) { t.Stream = appendTail(t.Stream, e.Text) })

	case agentkit.Thinking:
		if e.Frame == 0 {
			return editAssistant(v, func(b *Block) { b.Think += e.Text })
		}
		return editTool(v, e.Frame, func(t *Tool) { t.Stream = appendTail(t.Stream, e.Text) })

	case agentkit.ToolStart:
		return upsertTool(v, e.Frame, Tool{
			ID:      e.ID,
			Parent:  e.Frame,
			Name:    e.Tool,
			Args:    e.Args,
			Running: true,
			Started: now,
		})

	case agentkit.ToolEnd:
		return upsertTool(v, e.Frame, Tool{
			ID:       e.ID,
			Parent:   e.Frame,
			Name:     e.Tool,
			Args:     e.Args,
			Result:   e.Result,
			Err:      errText(e.Err),
			Duration: e.Duration,
		})

	case agentkit.TurnEnd:
		// A nested turn is closed by its enclosing ToolEnd; only the top-level one ends the view.
		if e.Frame != 0 {
			return v
		}
		v = editAssistant(v, func(b *Block) {
			b.Err = errText(e.Err)
			b.Cancelled = errors.Is(e.Err, context.Canceled)
			b.Tokens = e.Tokens.Total
			b.Pending = false
		})
		v.Running = false
		return v
	}
	return v
}

// PushUser echoes the user's message as its own block. Running is the caller's to set — a submit
// and a replayed in-flight input push the same block for different reasons.
func PushUser(v View, text string) View {
	return View{Blocks: append(slices.Clone(v.Blocks), Block{Role: User, Text: text}), Running: v.Running}
}

// PushNotice appends the UI's own line: a notification, a slash command's answer, an error.
func PushNotice(v View, text string) View {
	return View{Blocks: append(slices.Clone(v.Blocks), Block{Role: Notice, Text: text}), Running: v.Running}
}

// openAssistant opens a fresh pending assistant block unless the last one already is one.
func openAssistant(v View) View {
	if last := lastBlock(v); last != nil && last.Role == Assistant && last.Pending {
		return v
	}
	return View{Blocks: append(slices.Clone(v.Blocks), Block{Role: Assistant, Pending: true}), Running: v.Running}
}

// editAssistant applies edit to the last block iff it is an assistant turn. Anything else — an
// event arriving before its TurnStart, or after a notice — is dropped rather than misfiled.
func editAssistant(v View, edit func(*Block)) View {
	i := len(v.Blocks) - 1
	if i < 0 || v.Blocks[i].Role != Assistant {
		return v
	}
	blocks := slices.Clone(v.Blocks)
	blocks[i].Tools = slices.Clone(blocks[i].Tools)
	edit(&blocks[i])
	return View{Blocks: blocks, Running: v.Running}
}

// upsertTool records a call's start or end on the active assistant block, keyed by call id so an
// end updates its start in place. parent is the enclosing call (0 = top level) and yields Depth.
// An end keeps the start's Started so a renderer can still show when the call began.
func upsertTool(v View, parent uint64, t Tool) View {
	return editAssistant(v, func(b *Block) {
		t.Depth = depthUnder(b.Tools, parent)
		if i := indexOfTool(b.Tools, t.ID); i >= 0 {
			t.Started = b.Tools[i].Started
			t.Stream = b.Tools[i].Stream
			b.Tools[i] = t
			return
		}
		b.Tools = append(b.Tools, t)
	})
}

// editTool applies edit to the running call with the given id — the path for a sub-agent's nested
// tokens, which belong to the call that spawned them.
func editTool(v View, id uint64, edit func(*Tool)) View {
	return editAssistant(v, func(b *Block) {
		if i := indexOfTool(b.Tools, id); i >= 0 {
			edit(&b.Tools[i])
		}
	})
}

func indexOfTool(tools []Tool, id uint64) int {
	return slices.IndexFunc(tools, func(t Tool) bool { return t.ID == id })
}

// depthUnder returns the indent level for a call whose enclosing frame is parent.
func depthUnder(tools []Tool, parent uint64) int {
	if parent == 0 {
		return 0
	}
	if i := indexOfTool(tools, parent); i >= 0 {
		return tools[i].Depth + 1
	}
	return 0
}

// appendTail appends to a sub-agent's tail and trims the front to streamCap, on a rune boundary.
func appendTail(tail, text string) string {
	tail += text
	if len(tail) <= streamCap {
		return tail
	}
	tail = tail[len(tail)-streamCap:]
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	return tail
}

// snapshotBlocks builds the finished blocks from the persisted transcript and the per-turn tool
// forest. Turns are 1:1 with the transcript's user messages, so forest[turn] is the nested tree for
// that turn — restoring the same depth the live stream shows, including nested host-bridge and
// sub-agent calls the flat transcript loses. One assistant TURN spans several stored messages
// (assistant with tool_calls, tool result, assistant text) but is ONE block: consecutive assistant
// messages merge, broken by a user message, which advances the turn index.
func snapshotBlocks(msgs []agentkit.Message, forest [][]chat.ToolNode) []Block {
	var out []Block
	current := -1 // index of the turn's assistant block, or -1 before its first assistant message
	turn := -1
	for _, m := range msgs {
		switch m.Role {
		case agentkit.RoleTool, agentkit.RoleSystem:
			continue
		case agentkit.RoleUser:
			turn++
			current = -1
			out = append(out, Block{Role: User, Text: m.Content})
			continue
		}
		// A later assistant message of the same turn: merge its text. Its tools were already
		// covered by the turn's forest group, which spans every round of the turn.
		if current >= 0 {
			if m.Content != "" {
				if out[current].Text != "" {
					out[current].Text += "\n"
				}
				out[current].Text += m.Content
			}
			continue
		}
		var nodes []chat.ToolNode
		if turn >= 0 && turn < len(forest) {
			nodes = forest[turn]
		}
		out = append(out, Block{Role: Assistant, Text: m.Content, Tools: forestTools(nodes)})
		current = len(out) - 1
	}
	return out
}

// forestTools renders a finished turn's persisted nodes: depth is the length of the parent chain,
// ids are kept so the nesting matches the live path exactly. Nodes are in start order, so parents
// precede children.
func forestTools(nodes []chat.ToolNode) []Tool {
	if len(nodes) == 0 {
		return nil
	}
	byID := make(map[uint64]chat.ToolNode, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	depthOf := func(n chat.ToolNode) int {
		d, p := 0, n.Parent
		seen := map[uint64]bool{} // guard against a malformed cycle
		for p != 0 && !seen[p] {
			seen[p] = true
			parent, ok := byID[p]
			if !ok {
				break
			}
			d++
			p = parent.Parent
		}
		return d
	}
	tools := make([]Tool, 0, len(nodes))
	for _, n := range nodes {
		tools = append(tools, Tool{
			ID:       n.ID,
			Parent:   n.Parent,
			Depth:    depthOf(n),
			Name:     n.Tool,
			Args:     n.Args,
			Result:   n.Result,
			Err:      n.Err,
			Duration: time.Duration(n.DurationMs) * time.Millisecond,
		})
	}
	return tools
}

func lastBlock(v View) *Block {
	if len(v.Blocks) == 0 {
		return nil
	}
	return &v.Blocks[len(v.Blocks)-1]
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
