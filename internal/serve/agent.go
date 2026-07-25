package serve

import (
	"context"
	"encoding/json"

	"github.com/efuturetoday/nocturn/internal/agent"
)

// ── client → server (cmd) ────────────────────────────────────────────────────

// AgentList requests a workspace's declared-agent roster (the declarations, not their runs — runs are
// chats, listed via chat.list with kind:"agent").
type AgentList struct {
	Cmd string `json:"cmd"`
	Ws  string `json:"ws"`
}

// AgentFire triggers an agent run now. Task defaults to the scheduled prompt when empty. It is
// fire-and-forget: no reply — the run appears via chat.activity and streams over the agent-kind chat
// events, openable by chat.open with kind:"agent".
type AgentFire struct {
	Cmd  string `json:"cmd"`
	Ws   string `json:"ws"`
	Name string `json:"name"`
	Task string `json:"task,omitempty"`
}

// ── server → client (type) ───────────────────────────────────────────────────

// AgentInfo is one declared agent on the wire: its identity, schedule, autonomy and cage — what an
// "Agents" screen renders. Instructions (the system prompt) are deliberately omitted; the roster is a
// picker, not an editor.
type AgentInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	When        string   `json:"when,omitempty"`     // cron; "" = manual only
	Autonomy    string   `json:"autonomy"`           // "strict" | "guarded"
	Tools       []string `json:"tools,omitempty"`    // the cage
	Effort      string   `json:"effort,omitempty"`   // reasoning effort
	BudgetMs    int64    `json:"budgetMs,omitempty"` // per-run wall-clock; 0 = workspace default
}

// AgentListResult is a workspace's declared-agent roster, replying to agent.list.
type AgentListResult struct {
	Type   string      `json:"type"`
	Ws     string      `json:"ws"`
	Agents []AgentInfo `json:"agents"`
}

// agentInfos renders the declared agents to their wire form.
func agentInfos(agents []agent.Agent) []AgentInfo {
	out := make([]AgentInfo, 0, len(agents))
	for _, a := range agents {
		out = append(out, AgentInfo{
			Name:        a.Name,
			Description: a.Description,
			When:        a.When,
			Autonomy:    string(a.Autonomy),
			Tools:       a.Tools,
			Effort:      string(a.Effort),
			BudgetMs:    a.Budget.Milliseconds(),
		})
	}
	return out
}

// defaultAgentTask is the prompt a fired agent gets when the caller supplies none — the same one the
// cron scheduler uses, so a manual fire behaves like a scheduled one.
const defaultAgentTask = "Run your scheduled task now."

// agentCmd dispatches an agent.* action.
func (c *conn) agentCmd(ctx context.Context, cmd string, data []byte) {
	switch cmd {
	case "agent.list":
		var m AgentList
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad agent.list")
			return
		}
		ws, ok := c.workspace(ctx, m.Ws)
		if !ok {
			return
		}
		c.send(ctx, AgentListResult{Type: "agent.list", Ws: m.Ws, Agents: agentInfos(ws.Agents())})
	case "agent.fire":
		var m AgentFire
		if err := json.Unmarshal(data, &m); err != nil {
			c.badRequest(ctx, "bad agent.fire")
			return
		}
		ws, ok := c.workspace(ctx, m.Ws)
		if !ok {
			return
		}
		task := m.Task
		if task == "" {
			task = defaultAgentTask
		}
		// Fire-and-forget: FireAgent does not carry ctx into the run (the manager opens it on its own
		// background ctx), so the run survives this connection. A start-time error (unknown agent) is
		// reported; the run's own outcome surfaces via chat.activity + the agent-kind stream.
		if _, err := ws.FireAgent(ctx, m.Name, task); err != nil {
			c.failed(ctx, "agent.fire", err)
		}
	default:
		c.badRequest(ctx, "unknown action: "+cmd)
	}
}
