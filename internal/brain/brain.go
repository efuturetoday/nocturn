// Package brain is the agentic orchestration loop. Given a user request it asks
// a Model what to do; the Model either returns a final answer or a structured
// tool call. The brain runs the tool (each tool is a capability that goes
// through the gateway's broker + HITL + vault), feeds the result back, and loops
// until the Model answers or a step budget is exhausted.
//
// Tool calls are structured, not parsed out of free text: each tool declares a
// JSON-Schema for its arguments, and the Model returns the tool name plus a JSON
// argument string. Validating those arguments is each tool's own job (its Invoke
// unmarshals and checks them); a validation error is fed back to the Model so it
// can correct itself — nothing about an LLM is bulletproof, so the robustness is
// structure + schema guidance + our validation + retry.
//
// The Model is an interface so the brain is testable with a scripted mock — no
// network, no API key. Real providers implement Model.
package brain

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/efuturetoday/nocturn/internal/deadline"
)

// ErrMaxSteps is returned when the loop hits its step budget without the Model
// producing a final answer.
var ErrMaxSteps = errors.New("brain: max steps exceeded without a final answer")

// Message is one turn in the conversation. Role is "user", "assistant", or
// "tool".
type Message struct {
	Role    string
	Content string
}

// ToolCall is the Model's request to invoke a tool with JSON arguments.
type ToolCall struct {
	Tool string
	Args string // JSON
}

// Step is the Model's decision: a final Answer, or one or more ToolCalls to run.
// An empty ToolCalls means Answer is final. A model may request several calls in
// one turn; they are run SEQUENTIALLY (each through the Registry, each its own
// approval), never concurrently — so every effect stays a deliberate, single
// human decision.
type Step struct {
	Answer    string
	ToolCalls []ToolCall
}

// ToolSpec is the declaration a Model sees: the tool's name, a description, and
// a JSON Schema for its arguments.
type ToolSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// Tool is an invocable capability. Invoke receives the raw JSON arguments; it is
// responsible for unmarshalling and validating them (returning an error the
// brain feeds back to the Model on bad input).
type Tool struct {
	ToolSpec
	Invoke func(ctx context.Context, args string) (string, error)
}

// Model produces the next Step given the conversation and the available tools.
// If onToken is non-nil, the Model streams answer text through it as it arrives
// (tool-call decisions do not stream). onToken may be nil for no streaming.
type Model interface {
	Next(ctx context.Context, conv []Message, tools []ToolSpec, onToken func(string)) (Step, error)
}

// Brain runs the loop with a Model and a shared Registry of tools. OnToken, if
// set, receives answer text as it streams from the Model. Tool-call observation
// lives on the Registry — which both the Brain and the script interpreter
// dispatch through — so every tool call, model- or script-issued, is seen in one
// place; the Brain itself carries no per-tool UI hook.
type Brain struct {
	Model       Model
	Registry    *Registry
	MaxSteps    int
	ToolTimeout time.Duration // per-tool-call deadline; 0 = no limit
	OnToken     func(string)  // answer-token stream; nil = no streaming
}

const defaultMaxSteps = 8

// Run drives a single request to a final answer (fresh conversation).
func (b *Brain) Run(ctx context.Context, request string) (string, error) {
	ans, _, err := b.run(ctx, []Message{{Role: "user", Content: request}})
	return ans, err
}

// run drives the loop over conv until the Model gives a final answer or the step
// budget is exhausted, appending every turn (assistant/tool/final answer) to conv
// and returning the answer plus the extended conversation.
func (b *Brain) run(ctx context.Context, conv []Message) (string, []Message, error) {
	steps := b.MaxSteps
	if steps <= 0 {
		steps = defaultMaxSteps
	}

	for i := 0; i < steps; i++ {
		step, err := b.Model.Next(ctx, conv, b.Registry.Specs(), b.OnToken)
		if err != nil {
			return "", conv, err
		}
		if len(step.ToolCalls) == 0 {
			conv = append(conv, Message{Role: "assistant", Content: step.Answer})
			return step.Answer, conv, nil
		}
		// Run every requested call in order, feeding each result back before the
		// next — sequential, so multiple gated effects mean one approval at a time.
		for _, tc := range step.ToolCalls {
			conv = append(conv, Message{
				Role:    "assistant",
				Content: "call " + tc.Tool + "(" + tc.Args + ")",
			})
			conv = append(conv, Message{Role: "tool", Content: b.invoke(ctx, tc)})
		}
	}
	return "", conv, ErrMaxSteps
}

// Conversation is a multi-turn exchange with a Brain: it carries the history
// across turns so the model has the full context each time. It owns only the
// message history — the session lifecycle (epoch, guard) lives one layer out in
// internal/agent, which drives this.
type Conversation struct {
	brain *Brain
	conv  []Message
}

// NewConversation starts an empty conversation on this Brain.
func (b *Brain) NewConversation() *Conversation { return &Conversation{brain: b} }

// Send adds the user's input, runs the loop to a final answer, and keeps the
// whole exchange in the conversation's history.
func (c *Conversation) Send(ctx context.Context, input string) (string, error) {
	c.conv = append(c.conv, Message{Role: "user", Content: input})
	ans, conv, err := c.brain.run(ctx, c.conv)
	c.conv = conv
	return ans, err
}

// invoke runs a tool call through the shared Registry (which emits the observer
// events) and returns the content to feed back to the Model — the tool's output,
// or an "error: ..." string on failure (an unknown tool or a validation/execution
// error is reported, not fatal, so the Model can correct itself).
func (b *Brain) invoke(ctx context.Context, tc ToolCall) string {
	if b.ToolTimeout > 0 {
		var cancel context.CancelFunc
		// A pausable budget so an out-of-band approval inside the tool doesn't
		// burn the per-tool deadline (hitl pauses it during the human wait).
		ctx, cancel = deadline.WithBudget(ctx, b.ToolTimeout)
		defer cancel()
	}
	out, err := b.Registry.Invoke(ctx, tc.Tool, tc.Args)
	if err != nil {
		return "error: " + timeoutCause(ctx, err).Error()
	}
	return out
}

// timeoutCause replaces a context-cancellation error with the context's cause
// (e.g. deadline exceeded). A pausable budget cancels via context.WithCancelCause,
// so ctx.Err reports only Canceled; the real reason is the cause — the model
// should be told "deadline exceeded", not "canceled".
func timeoutCause(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) {
		if cause := context.Cause(ctx); cause != nil && cause != context.Canceled {
			return cause
		}
	}
	return err
}

// Phase marks whether a ToolEvent is the start or the end of an invocation.
type Phase int

const (
	ToolStart Phase = iota
	ToolEnd
)

// ToolEvent is emitted by a Registry around every tool invocation — model- or
// script-issued — so one observer sees all tool calls, nested by call order (a
// script's calls arrive between the ToolStart and ToolEnd of its code.run).
type ToolEvent struct {
	Tool   string
	Args   string // JSON, as the caller supplied it (model args or script args)
	Phase  Phase
	Result string // ToolEnd only
	Err    error  // ToolEnd only (e.g. gateway.ErrDenied for a denied effect)
}

// Registry is the one place tool calls are dispatched: it maps names to Tools,
// hands their specs to the Model, and runs a named tool's Invoke. It is shared
// by the Brain (model-issued calls) and the script interpreter (script-issued
// calls), so its OnCall observer sees every tool call from both, nested by call
// order. The tools map is set up before any Invoke and not mutated during one,
// so concurrent reads need no lock.
type Registry struct {
	tools  map[string]Tool
	OnCall func(ToolEvent) // observability sink; nil = off
}

// NewRegistry builds a Registry over the given tools. A nil slice yields an empty
// registry (every call reports "unknown tool"), which is convenient for tests.
func NewRegistry(tools []Tool) *Registry {
	reg := make(map[string]Tool, len(tools))
	for _, t := range tools {
		reg[t.Name] = t
	}
	return &Registry{tools: reg}
}

// Add registers a tool after construction — used for code.run, which needs the
// Registry to exist first so the interpreter can dispatch back into it.
func (r *Registry) Add(t Tool) { r.tools[t.Name] = t }

// Specs returns the tool declarations for the Model, sorted by name.
func (r *Registry) Specs() []ToolSpec {
	specs := make([]ToolSpec, 0, len(r.tools))
	for _, t := range r.tools {
		specs = append(specs, t.ToolSpec)
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs
}

// Invoke looks up a tool by name and runs it, emitting a ToolStart before and a
// ToolEnd after (carrying the result/error). An unknown tool is reported as an
// error the caller can surface — not fatal. The observer is fail-open.
func (r *Registry) Invoke(ctx context.Context, name, args string) (out string, err error) {
	r.emit(ToolEvent{Tool: name, Args: args, Phase: ToolStart})
	tool, ok := r.tools[name]
	if !ok {
		err = errors.New("unknown tool " + name)
	} else {
		out, err = tool.Invoke(ctx, args)
	}
	r.emit(ToolEvent{Tool: name, Args: args, Phase: ToolEnd, Result: out, Err: err})
	return out, err
}

func (r *Registry) emit(ev ToolEvent) {
	if r.OnCall != nil {
		r.OnCall(ev)
	}
}
