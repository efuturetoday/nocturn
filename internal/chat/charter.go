package chat

import (
	"time"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// Charter is a chat's construction spec: who it is and what it may do. The workspace
// is the only Charter factory — it mints the root charter (full tools, the persona,
// an unrestricted authority over the workspace grants) and compiles an agent
// declaration into its own (filtered tools, its Instructions, its declared
// restrictions). Who builds the Charter is the ONLY difference between a user chat
// and an agent run; after construction both are the same Chat.
type Charter struct {
	Tools     *tool.Registry    // the chat's COMPLETE toolset (already filtered for an agent)
	System    string            // system prompt: workspace persona, or the agent's Instructions
	Authority gateway.Authority // what Scope.Bind enforces every turn
	Budget    time.Duration     // wall-clock per turn (deadline.WithBudget); 0 = none
	Effort    brain.Effort      // default reasoning effort per turn (a per-message value overrides); "" = global default
}
