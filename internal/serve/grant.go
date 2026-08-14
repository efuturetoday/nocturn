package serve

import (
	"context"
	"encoding/json"

	"github.com/efuturetoday/nocturn/internal/workspace"
)

// The permissions a household has already given, and a way to take one back.
//
// Everything else on this wire is about what a workspace is MADE of — its skills, its servers, its
// plugins. This is about what it may DO without asking again, which is the other half of the same
// question and the half that was invisible: the gate asks on a new action and remembers the answer,
// and remembering is where authority quietly accumulates. A grant given for a reason nobody wrote
// down outlives the reason.
//
// Both actions take `manage`. Listing is not merely reading an inventory — the set of hosts a
// household approved says what it does — and revoking is a change to what the assistant may do.

// GrantList requests a workspace's standing approvals (client → server).
type GrantList struct {
	Cmd string `json:"cmd"`
	Ws  string `json:"ws"`
}

// GrantForget revokes one standing approval (client → server).
type GrantForget struct {
	Cmd    string `json:"cmd"`
	Ws     string `json:"ws"`
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

// GrantInfo is one standing approval, as a client shows it.
type GrantInfo struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	// Durable says it survives a restart. The distinction is the whole of what a person needs to
	// judge one: "until this daemon stops" and "forever" are different answers, and only the second
	// accumulates.
	Durable bool `json:"durable"`
}

// GrantListResult answers grant.list, and is broadcast after a revocation (server → client).
type GrantListResult struct {
	Type  string      `json:"type"`
	Ws    string      `json:"ws"`
	Items []GrantInfo `json:"items"`
}

// grantCmd dispatches a grant.* action.
func (c *conn) grantCmd(ctx context.Context, cmd string, data []byte) {
	if !c.can.manage {
		c.badRequest(ctx, "this device may not review the household's permissions")
		return
	}

	switch cmd {
	case "grant.list":
		var m GrantList
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad grant.list")
			return
		}
		ws, ok := c.workspace(ctx, m.Ws)
		if !ok {
			return
		}
		c.send(ctx, grantList(ws))

	case "grant.forget":
		var m GrantForget
		if err := json.Unmarshal(data, &m); err != nil || m.Kind == "" || m.Target == "" {
			c.badRequest(ctx, "bad grant.forget")
			return
		}
		ws, ok := c.workspace(ctx, m.Ws)
		if !ok {
			return
		}
		// A grant that was not there is not an error: two devices revoking the same one is an
		// ordinary race, and the answer both should get is the list without it.
		if ws.ForgetGrant(m.Kind, m.Target) {
			c.log.Info("revoked a standing permission",
				"ws", ws.Name(), "kind", m.Kind, "target", m.Target)
		}
		// To everyone, not just here: a permission is household state, and a second device showing a
		// grant that no longer exists would offer to revoke nothing.
		c.hub.broadcast(grantList(ws))

	default:
		c.badRequest(ctx, "unknown action: "+cmd)
	}
}

// grantList renders a workspace's standing approvals.
func grantList(ws *workspace.Workspace) GrantListResult {
	standing := ws.Grants()
	items := make([]GrantInfo, 0, len(standing))
	for _, s := range standing {
		items = append(items, GrantInfo{Kind: s.Grant.Kind, Target: s.Grant.Target, Durable: s.Durable})
	}
	return GrantListResult{Type: "grant.list", Ws: ws.Name(), Items: items}
}
