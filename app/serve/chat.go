package serve

import (
	"context"
	"encoding/json"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/app/chat"
)

// ── client → server (cmd) ────────────────────────────────────────────────────

// ChatSubmit sends a message to the chat with id ID — the client MINTS the id (crypto-random hex),
// so the request is self-identifying: an unknown id starts that chat (create-on-first-submit), a
// known one appends. No server-minted id, no chat.opened round-trip, no correlation problem. The
// server validates the id (ValidID) before it is ever a filesystem key.
// Kind selects which chat store a command targets: "" (or "user") the user chats, "agent" the agent
// runs. The client sends it on every store-addressed chat.* command, so the one handler set serves
// both managers; the client never needs to derive the store (it holds the kind statelessly, per
// conversation). The only agent-specific wire beyond this is the roster (agent.list) and the trigger
// (agent.fire).
type ChatSubmit struct {
	Cmd  string `json:"cmd"`
	Ws   string `json:"ws"`
	Kind string `json:"kind"`
	Text string `json:"text"`
	ID   string `json:"id"`
}

// ChatOpen resumes a chat in workspace Ws: the server replies with a ChatSnapshot, then streams turns.
type ChatOpen struct {
	Cmd  string `json:"cmd"`
	Ws   string `json:"ws"`
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// ChatList requests a workspace's chat list; Kind selects the store ("" user | "agent" runs).
type ChatList struct {
	Cmd  string `json:"cmd"`
	Ws   string `json:"ws"`
	Kind string `json:"kind"`
}

// ChatCancel aborts a chat's running turn (id-addressed; the chat and session stay open).
type ChatCancel struct {
	Cmd  string `json:"cmd"`
	Ws   string `json:"ws"`
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// ChatMarkRead advances a chat's shared read cursor in the Kind-selected store.
type ChatMarkRead struct {
	Cmd  string `json:"cmd"`
	Ws   string `json:"ws"`
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// ── server → client (type) ───────────────────────────────────────────────────

// The streaming events all carry ChatID: every live session broadcasts to EVERY device (no
// per-connection subscription), so the client routes each event to the right chat and drops those
// for a chat it isn't showing.

// ChatToken is one answer-text delta.
type ChatToken struct {
	Type   string `json:"type"`
	ChatID string `json:"chatId"`
	Frame  uint64 `json:"frame"`
	Text   string `json:"text"`
}

// ChatThinking is one reasoning-text delta.
type ChatThinking struct {
	Type   string `json:"type"`
	ChatID string `json:"chatId"`
	Frame  uint64 `json:"frame"`
	Text   string `json:"text"`
}

// ChatTool is a tool call's start or end.
type ChatTool struct {
	Type       string `json:"type"`
	ChatID     string `json:"chatId"`
	Phase      string `json:"phase"` // "start" | "end"
	Frame      uint64 `json:"frame"`
	ID         uint64 `json:"id"`
	Tool       string `json:"tool"`
	Args       string `json:"args"`
	Result     string `json:"result,omitempty"`
	Err        string `json:"err,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"` // end only — the call's wall-clock (server-measured)
}

// ChatTurnStart opens a turn: the client starts a fresh assistant bubble from it, so the answer
// bubble comes deterministically from the stream — a locally-sent turn and a backend-initiated one
// (wake resume, agent run, another device) render identically.
type ChatTurnStart struct {
	Type   string `json:"type"`
	ChatID string `json:"chatId"`
	Frame  uint64 `json:"frame"`
}

// ChatTurnEnd closes a turn, with the stop reason (if any) and the turn's total tokens.
type ChatTurnEnd struct {
	Type   string `json:"type"`
	ChatID string `json:"chatId"`
	Frame  uint64 `json:"frame"`
	Err    string `json:"err,omitempty"`
	Tokens int    `json:"tokens"`
}

// ChatSnapshot is a chat's persisted transcript plus its per-turn tool forest, sent on open so the
// client can render both the conversation and the nested tool calls (Tools[k] belongs to the k-th
// turn). Tools reconstructs nesting the flat transcript can't — nested and sub-agent calls.
//
// The running turn (if any) is NOT yet in the transcript. Rather than ship a server-materialized
// render model, the snapshot carries it as raw material the client folds with its ONE reducer:
// InflightInput is the user's message (recorded on submit, not a stream event) and InflightEvents are
// the turn's wire events so far — the same events the live broadcast delivers. The client replays them
// so a reopen mid-turn renders by the same path as the live stream. InflightRunning gates it (a turn
// can be running with input recorded before its first event streams).
type ChatSnapshot struct {
	Type            string             `json:"type"`
	ID              string             `json:"id"`
	Messages        []agentkit.Message `json:"messages"`
	Tools           [][]chat.ToolNode  `json:"tools"`
	InflightRunning bool               `json:"inflightRunning,omitempty"`
	InflightInput   string             `json:"inflightInput,omitempty"`
	InflightEvents  []any              `json:"inflightEvents,omitempty"`
}

// ChatActivity is pushed to every device when a chat changes (a turn ends, a markRead) so their
// lists update — its unread dot raises or clears without the device streaming that chat.
type ChatActivity struct {
	Type string    `json:"type"`
	Ws   string    `json:"ws"`
	Chat chat.Meta `json:"chat"`
}

// ChatListResult is a workspace's chat list, replying to chat.list. Kind echoes the requested store
// ("" user | "agent") so a client that lists both routes each result to the right view.
type ChatListResult struct {
	Type  string      `json:"type"`
	Ws    string      `json:"ws"`
	Kind  string      `json:"kind,omitempty"`
	Chats []chat.Meta `json:"chats"`
}

// chat dispatches a chat.* action.
func (c *conn) chat(ctx context.Context, cmd string, data []byte) {
	switch cmd {
	case "chat.submit":
		var m ChatSubmit
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad chat.submit")
			return
		}
		c.chatSubmit(ctx, m)
	case "chat.open":
		var m ChatOpen
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad chat.open")
			return
		}
		c.chatOpen(ctx, m)
	case "chat.list":
		var m ChatList
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad chat.list")
			return
		}
		c.chatList(ctx, m)
	case "chat.cancel":
		var m ChatCancel
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad chat.cancel")
			return
		}
		if !c.requireKind(ctx, m.Kind) {
			return
		}
		if ws, ok := c.workspace(ctx, m.Ws); ok {
			ws.ChatManager(m.Kind).Cancel(m.ID)
		}
	case "chat.markRead":
		var m ChatMarkRead
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad chat.markRead")
			return
		}
		if !c.requireKind(ctx, m.Kind) {
			return
		}
		if ws, ok := c.workspace(ctx, m.Ws); ok {
			ws.MarkRead(m.Kind, m.ID)
		}
	default:
		c.badRequest(ctx, "unknown action: "+cmd)
	}
}

func (c *conn) chatSubmit(ctx context.Context, m ChatSubmit) {
	if !c.requireKind(ctx, m.Kind) {
		return
	}
	ws, ok := c.workspace(ctx, m.Ws)
	if !ok {
		return
	}
	// Client-minted id: validate it (never trust a client id as a filesystem key), then submit. An
	// unknown id starts a fresh chat, a known one appends. The turn's events reach every device via
	// the Manager's broadcast, so this handler touches NO connection state and owns no session.
	if !chat.ValidID(m.ID) {
		c.badRequest(ctx, "bad chat id")
		return
	}
	// Submit (not Open().Submit) so the Manager records the input as the in-flight turn's user message —
	// a device reopening before the turn ends still sees the message and the working state.
	ws.ChatManager(m.Kind).Submit(m.ID, m.Text)
}

func (c *conn) chatOpen(ctx context.Context, m ChatOpen) {
	if !c.requireKind(ctx, m.Kind) {
		return
	}
	ws, ok := c.workspace(ctx, m.Ws)
	if !ok {
		return
	}
	mgr := ws.ChatManager(m.Kind)
	msgs, err := mgr.Transcript(m.ID)
	if err != nil {
		c.failed(ctx, "open", err)
		return
	}
	if msgs == nil {
		msgs = []agentkit.Message{} // the wire carries [] not null
	}
	tools, err := mgr.Tools(m.ID)
	if err != nil {
		c.failed(ctx, "open", err)
		return
	}
	if tools == nil {
		tools = [][]chat.ToolNode{} // the wire carries [] not null
	}
	// The persisted transcript + forest are finished turns; the in-flight turn (if any) is NOT yet in
	// them, so hand it over separately — else a reopen mid-turn would drop the client's own message and
	// the working state. Its events are rendered to the SAME wire form as the live broadcast (chatEvent),
	// so the client folds reopen + live by one path. Live events keep streaming on top of this.
	snap := ChatSnapshot{Type: "chat.snapshot", ID: m.ID, Messages: msgs, Tools: tools}
	if inf := mgr.Inflight(m.ID); inf.Running {
		snap.InflightRunning = true
		snap.InflightInput = inf.Input
		events := make([]any, 0, len(inf.Events))
		for _, ev := range inf.Events {
			if wire, ok := chatEvent(m.ID, ev); ok {
				events = append(events, wire)
			}
		}
		snap.InflightEvents = events
	}
	c.send(ctx, snap)
}

func (c *conn) chatList(ctx context.Context, m ChatList) {
	if !c.requireKind(ctx, m.Kind) {
		return
	}
	ws, ok := c.workspace(ctx, m.Ws)
	if !ok {
		return
	}
	metas, err := ws.ChatManager(m.Kind).List()
	if err != nil {
		c.failed(ctx, "list", err)
		return
	}
	c.send(ctx, ChatListResult{Type: "chat.list", Ws: m.Ws, Kind: m.Kind, Chats: metas})
}

// chatEvent renders one agentkit event as a wire chat.* message tagged with its chat id, for
// broadcast to every device (the client routes by chatId). TurnStart — and any event with no wire
// form — is skipped (ok=false).
func chatEvent(chatID string, ev agentkit.Event) (any, bool) {
	switch e := ev.(type) {
	case agentkit.TurnStart:
		return ChatTurnStart{Type: "chat.turnStart", ChatID: chatID, Frame: e.Frame}, true
	case agentkit.Token:
		return ChatToken{Type: "chat.token", ChatID: chatID, Frame: e.Frame, Text: e.Text}, true
	case agentkit.Thinking:
		return ChatThinking{Type: "chat.thinking", ChatID: chatID, Frame: e.Frame, Text: e.Text}, true
	case agentkit.ToolStart:
		return ChatTool{Type: "chat.tool", ChatID: chatID, Phase: "start", Frame: e.Frame, ID: e.ID, Tool: e.Tool, Args: e.Args}, true
	case agentkit.ToolEnd:
		return ChatTool{Type: "chat.tool", ChatID: chatID, Phase: "end", Frame: e.Frame, ID: e.ID, Tool: e.Tool, Args: e.Args, Result: e.Result, Err: errText(e.Err), DurationMs: e.Duration.Milliseconds()}, true
	case agentkit.TurnEnd:
		return ChatTurnEnd{Type: "chat.turnEnd", ChatID: chatID, Frame: e.Frame, Err: errText(e.Err), Tokens: e.Tokens.Total}, true
	}
	return nil, false
}
