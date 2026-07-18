package chat

import (
	"context"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/gateway"
)

// Once runs one task in a throwaway chat under charter — construction and turn
// ceremony IDENTICAL to a live Chat (scope minted from the charter's Authority,
// Bind + skills + budget in turn); it never persists and closes after the one
// turn (revoking its scope, so its session grants die with it).
//
// It is the shape of an attended in-chat agent spawn: called from a parent
// chat's turn, ctx already carries the parent's activity and approval sinks, so
// the child streams into the parent chat and its approvals surface there —
// while the child's OWN scope.Bind overrides the chat-level authority with the
// agent charter's (tools filtered at charter construction, policy/cage
// tightened, its own grants).
func Once(ctx context.Context, engine *brain.Brain, guard *gateway.Guard, ch Charter, task string) (string, error) {
	c := New(engine, guard, Meta{}, ch)
	defer c.Close()
	// No loop is started, so the construction-time state is the turn's state.
	return c.turn(ctx, turnState{conv: c.conv, scope: c.scope, skills: c.skills}, task)
}
