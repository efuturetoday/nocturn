package serve

import (
	"context"
	"encoding/json"
)

// ── server → client ──────────────────────────────────────────────────────────

// ApprovalRequest presents a pending out-of-band approval: an intent to render and choice labels
// (answer with the chosen index via approval.resolve, or -1 to deny).
type ApprovalRequest struct {
	Type    string   `json:"type"`
	ID      string   `json:"id"`
	Frame   uint64   `json:"frame,omitempty"`  // the tool call this approval is for (0 = not tool-scoped)
	ChatID  string   `json:"chatId,omitempty"` // the chat/agent run whose turn raised it (for provenance)
	Intent  string   `json:"intent"`
	Options []string `json:"options"`
}

// ApprovalResolved tells the client a pending approval is concluded — answered (here or on another
// device), timed out, or no longer needed — so it clears the prompt.
type ApprovalResolved struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// ── client → server ──────────────────────────────────────────────────────────

// ApprovalResolve answers a pending approval: the chosen option index, or -1 to deny.
type ApprovalResolve struct {
	Cmd    string `json:"cmd"`
	ID     string `json:"id"`
	Choice int    `json:"choice"`
}

// approval dispatches an approval.* action.
func (c *conn) approval(ctx context.Context, cmd string, data []byte) {
	switch cmd {
	case "approval.resolve":
		var m ApprovalResolve
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad approval.resolve")
			return
		}
		if c.broker == nil {
			// A class that may not approve is handed no broker at all (see serve.go), so this is a
			// device answering a question it is never shown. Refuse rather than dereference: the
			// least-trusted class must not be able to end its own connection's handler.
			c.badRequest(ctx, "this device may not answer approvals")
			return
		}
		c.broker.Resolve(m.ID, m.Choice)
	default:
		c.badRequest(ctx, "unknown action: "+cmd)
	}
}

// Approval implements hitl.Sink: it forwards a pending approval to this client. The broker supplies
// the ctx (the connection's or the asking turn's), so none is stored on the conn.
func (c *conn) Approval(ctx context.Context, id string, frame uint64, chatID, intent string, options []string) {
	c.send(ctx, ApprovalRequest{Type: "approval.request", ID: id, Frame: frame, ChatID: chatID, Intent: intent, Options: options})
}

// Resolved implements hitl.Sink: it tells this client to clear a concluded approval.
func (c *conn) Resolved(ctx context.Context, id string) {
	c.send(ctx, ApprovalResolved{Type: "approval.resolved", ID: id})
}
