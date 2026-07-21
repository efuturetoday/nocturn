package serve

import (
	"context"
	"encoding/json"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/app/chat"
)

// ── client → server (cmd) ────────────────────────────────────────────────────

// ChatSubmit sends a message to the active chat (starting a new one in workspace Ws if none is active).
type ChatSubmit struct {
	Cmd  string `json:"cmd"`
	Ws   string `json:"ws"`
	Text string `json:"text"`
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

// ChatCancel aborts the active chat's running turn (the chat and session stay open).
type ChatCancel struct {
	Cmd string `json:"cmd"`
}

// ChatMarkRead advances a chat's shared read cursor (clears its unread state on every device).
type ChatMarkRead struct {
	Cmd string `json:"cmd"`
	Ws  string `json:"ws"`
	ID  string `json:"id"`
}

// ── server → client (type) ───────────────────────────────────────────────────

// ChatToken is one answer-text delta.
type ChatToken struct {
	Type  string `json:"type"`
	Frame uint64 `json:"frame"`
	Text  string `json:"text"`
}

// ChatThinking is one reasoning-text delta.
type ChatThinking struct {
	Type  string `json:"type"`
	Frame uint64 `json:"frame"`
	Text  string `json:"text"`
}

// ChatTool is a tool call's start or end.
type ChatTool struct {
	Type   string `json:"type"`
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
	Frame  uint64 `json:"frame"`
	Err    string `json:"err,omitempty"`
	Tokens int    `json:"tokens"`
}

// ChatOpened tells the client the id of the chat it just started (a new chat from chat.submit), so
// it can bind its active-chat id — a resume already knows the id it opened.
type ChatOpened struct {
	Type string `json:"type"`
	ID   string `json:"id"`
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
		if c.active != nil {
			c.active.Cancel()
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
	if c.active != nil {
		c.active.Submit(m.Text)
		return
	}
	ws, ok := c.workspace(ctx, m.Ws)
	if !ok {
		return
	}
	id, sess := ws.Chats().Start(ctx, m.Text)
	c.send(ctx, ChatOpened{Type: "chat.opened", ID: id})
	c.activate(ctx, sess)
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
	c.send(ctx, ChatSnapshot{Type: "chat.snapshot", ID: m.ID, Messages: msgs})
	c.activate(ctx, ws.Chats().Open(ctx, m.ID))
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

// activate makes sess the connection's live chat: the previous one is closed (its transcript is
// persisted and reloads on open), and a render goroutine forwards the new session's events until
// the session or the connection ends.
func (c *conn) activate(ctx context.Context, sess *agentkit.Session) {
	if c.active != nil {
		c.active.Close()
	}
	c.active = sess
	go c.render(ctx, sess)
}

// render forwards one session's event stream to the client as chat.* events until it closes.
func (c *conn) render(ctx context.Context, sess *agentkit.Session) {
	for ev := range sess.Subscribe() {
		switch e := ev.(type) {
		case agentkit.Token:
			c.send(ctx, ChatToken{Type: "chat.token", Frame: e.Frame, Text: e.Text})
		case agentkit.Thinking:
			c.send(ctx, ChatThinking{Type: "chat.thinking", Frame: e.Frame, Text: e.Text})
		case agentkit.ToolStart:
			c.send(ctx, ChatTool{Type: "chat.tool", Phase: "start", Frame: e.Frame, ID: e.ID, Tool: e.Tool, Args: e.Args})
		case agentkit.ToolEnd:
			c.send(ctx, ChatTool{Type: "chat.tool", Phase: "end", Frame: e.Frame, ID: e.ID, Tool: e.Tool, Args: e.Args, Result: e.Result, Err: errText(e.Err)})
		case agentkit.TurnEnd:
			c.send(ctx, ChatTurnEnd{Type: "chat.turnEnd", Frame: e.Frame, Err: errText(e.Err), Tokens: e.Tokens.Total})
		}
	}
}
