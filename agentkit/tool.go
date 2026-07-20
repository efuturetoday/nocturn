package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync/atomic"
	"time"
)

// Tool-name / description limits, verified against OpenAI function calling and Anthropic tool use
// (the name regex is identical; both reject a bad name with a 400). These are HARD spec rules
// enforced at construction — an invalid tool is never created — NOT advisory diagnostics.
const (
	MaxToolNameLen  = 64
	MaxToolDescLen  = 1024 // OpenAI hard-rejects a longer description; Anthropic tolerates it.
	ToolNamePattern = `^[a-zA-Z0-9_-]{1,64}$`
)

var toolNameRe = regexp.MustCompile(ToolNamePattern)

// ToolSpec is a tool's model-facing declaration — only what the model needs to decide to call it.
// Runtime behavior (result budget, …) is NOT here; it lives on the Tool via options.
type ToolSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage // JSON Schema for args; nil = no-arg tool
}

// Validate enforces only the HARD rules — the ones a provider rejects with a 400: Name matches
// ToolNamePattern, and Parameters (if set) is well-formed JSON. Description LENGTH is NOT enforced
// here: an over-long description is a Warn diagnostic (OpenAI rejects above MaxToolDescLen, Anthropic
// tolerates it), not a hard deny. Validate does NOT check the schema semantically nor validate call
// arguments — argument validation is the tool's OWN job inside Call. The schema is otherwise
// pass-through: it goes to the provider to constrain generation.
func (s ToolSpec) Validate() error {
	if !toolNameRe.MatchString(s.Name) {
		return fmt.Errorf("agentkit: invalid tool name %q: must match %s", s.Name, ToolNamePattern)
	}
	if len(s.Parameters) > 0 && !json.Valid(s.Parameters) {
		return fmt.Errorf("agentkit: tool %q: malformed parameter schema", s.Name)
	}
	return nil
}

// Tool is the port. The consumer builds it; its guard sits in Call, because the consumer is the one
// who knows what the tool does. A Tool may hold state/deps as fields. The verb is Call because the
// model issues a ToolCall — calling the tool is the natural act.
type Tool interface {
	Spec() ToolSpec
	Call(ctx context.Context, args string) (string, error)
}

// ToolFunc is a tool's effect as a closure: raw JSON args in, a string result out. It is what
// NewTool wraps and what a Tool's Call ultimately runs.
type ToolFunc func(ctx context.Context, args string) (string, error)

// toolOptions collects the optional behavior a func-backed Tool can carry.
type toolOptions struct {
	parameters json.RawMessage
	maxChars   int
}

// ToolOption configures a Tool built by NewTool.
type ToolOption func(*toolOptions)

// WithSchema sets the JSON Schema for the tool's arguments (omit for a no-arg tool). It is passed
// through to the provider verbatim; only well-formedness is checked, not schema semantics.
func WithSchema(params json.RawMessage) ToolOption {
	return func(o *toolOptions) { o.parameters = params }
}

// WithMaxChars truncates the tool's result to n characters (0 = unbounded). The library enforces it
// around Call so a tool needn't self-truncate.
func WithMaxChars(n int) ToolOption {
	return func(o *toolOptions) { o.maxChars = n }
}

// funcTool adapts a closure + spec into a Tool.
type funcTool struct {
	spec     ToolSpec
	fn       ToolFunc
	maxChars int
}

func (t funcTool) Spec() ToolSpec { return t.spec }

func (t funcTool) Call(ctx context.Context, args string) (string, error) {
	out, err := t.fn(ctx, args)
	if err != nil {
		return out, err
	}
	return truncateChars(out, t.maxChars), nil
}

// NewTool builds a Tool from a closure, so a simple consumer needs no own type. It returns an error
// if the resulting spec is invalid (bad name, malformed schema) — an invalid tool is never created.
func NewTool(name, description string, fn ToolFunc, opts ...ToolOption) (Tool, error) {
	if fn == nil {
		return nil, errors.New("agentkit: nil tool func")
	}
	var cfg toolOptions
	for _, o := range opts {
		o(&cfg)
	}
	spec := ToolSpec{Name: name, Description: description, Parameters: cfg.parameters}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	return funcTool{spec: spec, fn: fn, maxChars: cfg.maxChars}, nil
}

// ToolSet is the tools available to a session, keyed by name — a plain named map, so index, range,
// len and delete all work; the domain methods just sit on top. Immutability and agent attenuation
// are by convention: Select returns a COPY, so an agent's subset simply lacks the tools it may not
// use (a missing key is unreachable) and cannot widen the parent.
type ToolSet map[string]Tool

// NewToolSet builds a ToolSet keyed by each tool's Spec().Name, validating every spec. It returns an
// error if any tool's spec is invalid or two tools share a name — so a ToolSet never holds an
// invalid or colliding tool. (Building the map literal directly bypasses this guarantee.)
func NewToolSet(tools ...Tool) (ToolSet, error) {
	set := make(ToolSet, len(tools))
	for _, t := range tools {
		if t == nil {
			return nil, errors.New("agentkit: nil tool")
		}
		spec := t.Spec()
		if err := spec.Validate(); err != nil {
			return nil, err
		}
		if _, dup := set[spec.Name]; dup {
			return nil, fmt.Errorf("agentkit: duplicate tool name %q", spec.Name)
		}
		set[spec.Name] = t
	}
	return set, nil
}

// Select returns a NEW ToolSet holding only tools whose name satisfies keep. A tool outside the
// subset is UNREACHABLE (Call reports "unknown"), a hard bound — not merely hidden from the model.
func (t ToolSet) Select(keep func(name string) bool) ToolSet {
	out := make(ToolSet)
	for name, tool := range t {
		if keep(name) {
			out[name] = tool
		}
	}
	return out
}

// Specs returns the tool declarations for the model, sorted by name.
func (t ToolSet) Specs() []ToolSpec {
	specs := make([]ToolSpec, 0, len(t))
	for _, tool := range t {
		specs = append(specs, tool.Spec())
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs
}

// Call looks up name and runs its Tool.Call. It assigns a fresh call-instance id (from a counter
// carried in ctx, so nested calls form a parent/child forest — the name is tool identity, the id is
// call identity for the same tool called several times or nested) and emits ToolStart/ToolEnd around
// the run. An unknown tool is a non-fatal error surfaced to the model.
func (t ToolSet) Call(ctx context.Context, name, args string) (string, error) {
	tool, ok := t[name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	parent := frameFrom(ctx)
	id := nextCallID(ctx)
	Emit(ctx, ToolStart{Frame: parent, ID: id, Tool: name, Args: args})
	start := time.Now()
	out, err := tool.Call(withFrame(ctx, id), args)
	Emit(ctx, ToolEnd{Frame: parent, ID: id, Tool: name, Args: args, Result: out, Err: err, Duration: time.Since(start)})
	return out, err
}

// --- call-instance id + frame plumbing (ctx-carried) ---

type counterKey struct{}
type frameKey struct{}

// withCounter installs the shared call-id counter into ctx at the top level; a nested run (a
// sub-agent) inherits the existing one so ids stay globally unique across the whole tree.
func withCounter(ctx context.Context) context.Context {
	if ctx.Value(counterKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, counterKey{}, new(atomic.Uint64))
}

func nextCallID(ctx context.Context) uint64 {
	c, _ := ctx.Value(counterKey{}).(*atomic.Uint64)
	if c == nil {
		return 0
	}
	return c.Add(1)
}

// frameFrom is the current enclosing call id (0 = top level / main agent).
func frameFrom(ctx context.Context) uint64 {
	f, _ := ctx.Value(frameKey{}).(uint64)
	return f
}

func withFrame(ctx context.Context, id uint64) context.Context {
	return context.WithValue(ctx, frameKey{}, id)
}

func truncateChars(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
