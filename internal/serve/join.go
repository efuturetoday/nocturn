package serve

import (
	"context"

	"github.com/efuturetoday/nocturn/internal/auth"
)

// JoinList requests the pending second-device joins (client → server). Only an already-paired
// connection reaches here — the /ws bearer gate — so returning the codes is safe.
type JoinList struct {
	Cmd string `json:"cmd"`
}

// JoinListResult carries the open joins and their codes for a human to relay (server → client).
type JoinListResult struct {
	Type  string             `json:"type"`
	Joins []auth.PendingJoin `json:"joins"`
}

// join dispatches a join.* action.
func (c *conn) join(ctx context.Context, cmd string) {
	switch cmd {
	case "join.list":
		c.send(ctx, JoinListResult{Type: "join.list", Joins: c.devices.PendingJoins()})
	default:
		c.badRequest(ctx, "unknown action: "+cmd)
	}
}
