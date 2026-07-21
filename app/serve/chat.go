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
type ChatSubmit struct {
	Cmd  string `json:"cmd"`
	Ws   string `json:"ws"`
	Text string `json:"text"`
	ID   string `json:"id"`
}

// ChatOpen resumes a chat in workspace Ws: the server replies with a ChatSnapshot, then streams turns.
type ChatOpen struct {
	Cmd string `json:"cmd"`
	Ws  string `json:"ws"`
	ID  string `json:"id"`
}

// ChatList requests a workspace's chat list.
type ChatList struct {
	Cmd string `json:"cmd"`
	Ws  string `json:"ws"`
}

// ChatCancel aborts a chat's running turn (id-addressed; the chat and session stay open).
type ChatCancel struct {
	Cmd string `json:"cmd"`
	Ws  string `json:"ws"`
	ID  string `json:"id"`
}

// ChatMarkRead advances a chat's shared read cursor (clears its unread state on every device).
type ChatMarkRead struct {
	Cmd string `json:"cmd"`
	Ws  string `json:"ws"`
	ID  string `json:"id"`
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
	Type   string `json:"type"`
	ChatID string `json:"chatId"`
	Phase  string `json:"phase"` // "start" | "end"
	Frame  uint64 `json:"frame"`
	ID     uint64 `json:"id"`
	Tool   string `json:"tool"`
	Args   string `json:"args"`
	Result string `json:"result,omitempty"`
	Err    string `json:"err,omitempty"`
}

// ChatTurnEnd closes a turn, with the stop reason (if any) and the turn's total tokens.
type ChatTurnEnd struct {
	Type   string `json:"type"`
	ChatID string `json:"chatId"`
	Frame  uint64 `json:"frame"`
	Err    string `json:"err,omitempty"`
	Tokens int    `json:"tokens"`
}

// ChatSnapshot is a chat's persisted transcript, sent on open so the client can render it.
type ChatSnapshot struct {
	Type     string             `json:"type"`
	ID       string             `json:"id"`
	Messages []agentkit.Message `json:"messages"`
}

// ChatActivity is pushed to every device when a chat changes (a turn ends, a markRead) so their
// lists update — its unread dot raises or clears without the device streaming that chat.
type ChatActivity struct {
	Type string    `json:"type"`
	Ws   string    `json:"ws"`
	Chat chat.Meta `json:"chat"`
}

// ChatListResult is a workspace's chat list, replying to chat.list.
type ChatListResult struct {
	Type  string      `json:"type"`
	Ws    string      `json:"ws"`
	Chats []chat.Meta `json:"chats"`
}

// chat dispatches a chat.* action.
func (c *conn) chat(ctx context.Context, cmd string, data []byte) {
	switch cmd {
	case "chat.submit":
		var m ChatSubmit
		if err := json.Unmarshal(data, &m); err != nil {
			c.send(ctx, newError("bad chat.submit"))
			return
		}
		c.chatSubmit(ctx, m)
	case "chat.open":
		var m ChatOpen
		if err := json.Unmarshal(data, &m); err != nil {
			c.send(ctx, newError("bad chat.open"))
			return
		}
		c.chatOpen(ctx, m)
	case "chat.list":
		var m ChatList
		if err := json.Unmarshal(data, &m); err != nil {
			c.send(ctx, newError("bad chat.list"))
			return
		}
		c.chatList(ctx, m.Ws)
	case "chat.cancel":
		var m ChatCancel
		if err := json.Unmarshal(data, &m); err != nil {
			c.send(ctx, newError("bad chat.cancel"))
			return
		}
		if ws, ok := c.workspace(ctx, m.Ws); ok {
			ws.Chats().Cancel(m.ID)
		}
	case "chat.markRead":
		var m ChatMarkRead
		if err := json.Unmarshal(data, &m); err != nil {
			c.send(ctx, newError("bad chat.markRead"))
			return
		}
		if ws, ok := c.workspace(ctx, m.Ws); ok {
			ws.MarkRead(m.ID)
		}
	default:
		c.send(ctx, newError("unknown action: "+cmd))
	}
}

func (c *conn) chatSubmit(ctx context.Context, m ChatSubmit) {
	ws, ok := c.workspace(ctx, m.Ws)
	if !ok {
		return
	}
	// Client-minted id: validate it (never trust a client id as a filesystem key), then submit. An
	// unknown id starts a fresh chat, a known one appends. The turn's events reach every device via
	// the Manager's broadcast, so this handler touches NO connection state and owns no session.
	if !chat.ValidID(m.ID) {
		c.send(ctx, newError("bad chat id"))
		return
	}
	ws.Chats().Open(m.ID).Submit(m.Text)
}

func (c *conn) chatOpen(ctx context.Context, m ChatOpen) {
	ws, ok := c.workspace(ctx, m.Ws)
	if !ok {
		return
	}
	msgs, err := ws.Chats().Transcript(m.ID)
	if err != nil {
		c.send(ctx, newError("open: "+err.Error()))
		return
	}
	if msgs == nil {
		msgs = []agentkit.Message{} // the wire carries [] not null
	}
	// Just the persisted transcript for the initial render — live tokens already stream to every
	// device via the Manager's broadcast (filtered client-side by chat id). No session is touched.
	c.send(ctx, ChatSnapshot{Type: "chat.snapshot", ID: m.ID, Messages: msgs})
}

func (c *conn) chatList(ctx context.Context, wsName string) {
	ws, ok := c.workspace(ctx, wsName)
	if !ok {
		return
	}
	metas, err := ws.Chats().List()
	if err != nil {
		c.send(ctx, newError("list: "+err.Error()))
		return
	}
	c.send(ctx, ChatListResult{Type: "chat.list", Ws: wsName, Chats: metas})
}

// chatEvent renders one agentkit event as a wire chat.* message tagged with its chat id, for
// broadcast to every device (the client routes by chatId). TurnStart — and any event with no wire
// form — is skipped (ok=false).
func chatEvent(chatID string, ev agentkit.Event) (any, bool) {
	switch e := ev.(type) {
	case agentkit.Token:
		return ChatToken{Type: "chat.token", ChatID: chatID, Frame: e.Frame, Text: e.Text}, true
	case agentkit.Thinking:
		return ChatThinking{Type: "chat.thinking", ChatID: chatID, Frame: e.Frame, Text: e.Text}, true
	case agentkit.ToolStart:
		return ChatTool{Type: "chat.tool", ChatID: chatID, Phase: "start", Frame: e.Frame, ID: e.ID, Tool: e.Tool, Args: e.Args}, true
	case agentkit.ToolEnd:
		return ChatTool{Type: "chat.tool", ChatID: chatID, Phase: "end", Frame: e.Frame, ID: e.ID, Tool: e.Tool, Args: e.Args, Result: e.Result, Err: errText(e.Err)}, true
	case agentkit.TurnEnd:
		return ChatTurnEnd{Type: "chat.turnEnd", ChatID: chatID, Frame: e.Frame, Err: errText(e.Err), Tokens: e.Tokens.Total}, true
	}
	return nil, false
}
