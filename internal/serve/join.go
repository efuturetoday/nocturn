package serve

import (
	"context"

	"github.com/efuturetoday/nocturn/internal/auth"
)

// JoinList requests the pending second-device joins (client → server), codes included.
//
// A bearer is not enough to see them. A join code IS an enrolment — whoever reads one can complete
// the join and walk away with a device — so this is gated on the same capability that guards POST
// /devices. Otherwise an appliance or the local command line, neither of which may enrol anything,
// could read a code and multiply the household anyway, which is exactly what handleEnrol's subset
// test exists to prevent.
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
		if !c.can.enrol {
			c.badRequest(ctx, "this device may not enrol others")
			return
		}
		c.send(ctx, JoinListResult{Type: "join.list", Joins: c.devices.PendingJoins()})
	default:
		c.badRequest(ctx, "unknown action: "+cmd)
	}
}
