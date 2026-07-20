package agentkit

import (
	"context"
	"encoding/json"
	"regexp"
)

// Tool-name / description limits, verified against OpenAI function calling and Anthropic tool
// use (the name regex is identical; both reject a bad name with a 400). These are HARD spec
// rules enforced at construction — an invalid tool is never created — NOT advisory diagnostics.
const (
	MaxToolNameLen  = 64
	MaxToolDescLen  = 1024 // OpenAI hard-rejects a longer description; Anthropic tolerates it.
	ToolNamePattern = `^[a-zA-Z0-9_-]{1,64}$`
)

var toolNameRe = regexp.MustCompile(ToolNamePattern)

// ToolSpec is a tool's model-facing declaration — only what the model needs to decide to call
// it. Runtime behavior (result budget, …) is NOT here; it lives on the Tool via options.
type ToolSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage // JSON Schema for args; nil = no-arg tool
}

// Validate enforces only the HARD rules — the ones a provider rejects with a 400: Name matches
// ToolNamePattern, and Parameters (if set) is well-formed JSON. Description LENGTH is NOT enforced
// here: an over-long description is a Warn diagnostic (OpenAI rejects above MaxToolDescLen,
// Anthropic tolerates it), not a hard deny. Validate does NOT check the schema semantically (that
// needs a schema-validator dependency) nor validate call arguments — argument validation is the
// tool's OWN job inside Call (unmarshal, check, feed the error back to the model, retry). The
// schema is otherwise pass-through: it goes to the provider to constrain generation.
func (s ToolSpec) Validate() error { panic("TODO") }

// Tool is the port. The consumer builds it; its guard sits in Call, because the consumer is the
// one who knows what the tool does. A Tool may hold state/deps as fields. The verb is Call
// because the model issues a ToolCall — calling the tool is the natural act.
type Tool interface {
	Spec() ToolSpec
	Call(ctx context.Context, args string) (string, error)
}

// toolOptions collects the optional behavior a func-backed Tool can carry.
type toolOptions struct {
	parameters json.RawMessage
	maxChars   int
}

// ToolOption configures a Tool built by NewTool.
type ToolOption func(*toolOptions)

// WithSchema sets the JSON Schema for the tool's arguments (omit for a no-arg tool). It is
// passed through to the provider verbatim; only well-formedness is checked, not schema semantics.
func WithSchema(params json.RawMessage) ToolOption { panic("TODO") }

// WithMaxChars truncates the tool's result to n characters (0 = unbounded). The library enforces
// it around Call so a tool needn't self-truncate.
func WithMaxChars(n int) ToolOption { panic("TODO") }

// NewTool builds a Tool from a closure, so a simple consumer needs no own type. It returns an
// error if the resulting spec is invalid (bad name, empty description, malformed schema) — an
// invalid tool is never created.
func NewTool(name, description string, fn func(ctx context.Context, args string) (string, error), opts ...ToolOption) (Tool, error) {
	panic("TODO")
}

// ToolSet is the tools available to a session, keyed by name — a plain named map, so index,
// range, len and delete all work; the domain methods just sit on top. Immutability and agent
// attenuation are by convention: Select returns a COPY, so an agent's subset simply lacks the
// tools it may not use (a missing key is unreachable) and cannot widen the parent.
type ToolSet map[string]Tool

// NewToolSet builds a ToolSet keyed by each tool's Spec().Name, validating every spec. It returns
// an error if any tool's spec is invalid or two tools share a name — so a ToolSet never holds an
// invalid or colliding tool. (Building the map literal directly bypasses this guarantee; use
// NewToolSet to get it.)
func NewToolSet(tools ...Tool) (ToolSet, error) { panic("TODO") }

// Select returns a NEW ToolSet holding only tools whose name satisfies keep.
func (t ToolSet) Select(keep func(name string) bool) ToolSet { panic("TODO") }

// Specs returns the tool declarations for the model, sorted by name.
func (t ToolSet) Specs() []ToolSpec { panic("TODO") }

// Call looks up name and runs its Tool.Call. It assigns a fresh call-instance id (from a counter
// carried in ctx, so nested calls form a parent/child forest — the name is tool identity, the id
// is call identity for the same tool called several times or nested) and emits ToolStart/ToolEnd
// around the run. An unknown tool is a non-fatal error surfaced to the model.
func (t ToolSet) Call(ctx context.Context, name, args string) (string, error) { panic("TODO") }
