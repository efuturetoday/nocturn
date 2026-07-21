package agentkit

// Role is a message author.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one conversation turn. A tool result is a RoleTool message whose ToolCallID
// links it to the call it answers (native id association, not positional). The optional fields carry
// omitempty so a persisted transcript stays clean — a plain message serializes to just role +
// content, without null tool-call clutter.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`  // set on an assistant message that issues calls
	ToolCallID string     `json:"toolCallID,omitempty"` // set on a tool-result message
	DurationMs int64      `json:"durationMs,omitempty"` // wall-clock of the tool call, on a tool-result message
}

// ToolCall is one model-issued call, carrying the id used to match its result.
type ToolCall struct {
	ID   string `json:"id"`
	Tool string `json:"tool"`
	Args string `json:"args"` // JSON
}

// Step is the LLM's output for one Next: either a final Answer or a batch of ToolCalls, plus the
// TokenCount of THIS round-trip (the adapter fills it from the provider response — no tokenizer).
type Step struct {
	Answer    string
	ToolCalls []ToolCall
	Tokens    TokenCount
}

// TokenCount is a prompt/completion token count. From a Step it is one round-trip; on TurnEnd it
// is the turn total (the sum of every round-trip = what you are BILLED, since history is re-sent
// each call). To gauge context-window fill instead, read the LAST round-trip's Prompt, not the sum.
type TokenCount struct {
	Prompt     int
	Completion int
	Total      int
}

// add accumulates o into t (used to sum a turn's round-trips).
func (t *TokenCount) add(o TokenCount) {
	t.Prompt += o.Prompt
	t.Completion += o.Completion
	t.Total += o.Total
}
