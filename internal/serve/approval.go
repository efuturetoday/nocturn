package serve

import (
	"context"
	"encoding/json"

	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/hitl"
)

// ── server → client ──────────────────────────────────────────────────────────

// ApprovalRequest presents a pending out-of-band approval as STRUCTURE, never as prose. Kind and
// Target are the gate action verbatim; Options are the answers this daemon minted for this exact
// approval. The device does the wording — a Kind is a closed set it maps to its own label, a Target
// is data it renders as data. Nothing here is composed by the model, and nothing here is a sentence
// that could bury one.
type ApprovalRequest struct {
	Type    string           `json:"type"`
	ID      string           `json:"id"`
	Frame   uint64           `json:"frame,omitempty"`  // the tool call this approval is for (0 = not tool-scoped)
	ChatID  string           `json:"chatId,omitempty"` // the chat/agent run whose turn raised it (for provenance)
	Kind    string           `json:"kind"`             // the gate axis, verbatim
	Target  string           `json:"target,omitempty"` // absent = a kind with no target
	Options []ApprovalOption `json:"options"`          // presentation order; never empty
}

// ApprovalOption is one answer on offer. ID is OPAQUE: the device echoes it back in approval.resolve
// and never mints one, so a device can only choose among grants the daemon itself offered. Recall is
// how long choosing it would be remembered. Widen is present only for a suggested WIDENING and
// carries the broader grant that answer would create — its presence is the whole "is this a
// widening?" question, so no layer has to infer it by comparing strings.
type ApprovalOption struct {
	ID     string         `json:"id"`
	Recall string         `json:"recall"` // "never" | "session" | "always"
	Widen  *ApprovalGrant `json:"widen,omitempty"`
}

// ApprovalGrant is a {kind,target} pattern on the wire — the same pair that lands in grants.json.
type ApprovalGrant struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

// ApprovalResolved tells the client a pending approval is concluded — answered (here or on another
// device), timed out, or no longer needed — so it clears the prompt.
type ApprovalResolved struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// ── client → server ──────────────────────────────────────────────────────────

// ApprovalResolve answers a pending approval with the id of the option chosen. Any value that is not
// one of the offered ids — the reserved "deny", an unknown id, or the empty string a truncated
// message leaves behind — refuses. There is no numeric index and no zero value that approves.
type ApprovalResolve struct {
	Cmd    string `json:"cmd"`
	ID     string `json:"id"`
	Option string `json:"option"`
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
		// An empty or unknown option is forwarded, not rejected: the broker refuses anything it did
		// not offer, so the one deny rule lives in one place and the approval never hangs open.
		c.broker.Resolve(m.ID, m.Option)
	default:
		c.badRequest(ctx, "unknown action: "+cmd)
	}
}

// Approval implements hitl.Sink: it forwards a pending approval to this client. The broker supplies
// the ctx (the connection's or the asking turn's), so none is stored on the conn.
func (c *conn) Approval(ctx context.Context, a hitl.Approval) {
	opts := make([]ApprovalOption, 0, len(a.Options))
	for _, o := range a.Options {
		w := ApprovalOption{ID: o.ID, Recall: recallName(o.Recall)}
		if o.Widens {
			w.Widen = &ApprovalGrant{Kind: o.Grant.Kind, Target: o.Grant.Target}
		}
		opts = append(opts, w)
	}
	c.send(ctx, ApprovalRequest{
		Type:    "approval.request",
		ID:      a.ID,
		Frame:   a.Frame,
		ChatID:  a.ChatID,
		Kind:    a.Action.Kind,
		Target:  a.Action.Target,
		Options: opts,
	})
}

// Resolved implements hitl.Sink: it tells this client to clear a concluded approval.
func (c *conn) Resolved(ctx context.Context, id string) {
	c.send(ctx, ApprovalResolved{Type: "approval.resolved", ID: id})
}

// recallName is the wire spelling of a recall. An unknown value names the narrowest one rather than
// inventing a wider promise for the device to render.
func recallName(r gate.Recall) string {
	switch r {
	case gate.RecallSession:
		return "session"
	case gate.RecallAlways:
		return "always"
	default:
		return "never"
	}
}

var _ hitl.Sink = (*conn)(nil)
