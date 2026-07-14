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

// Step is the Model's decision: a final Answer, or a ToolCall. A nil ToolCall
// means Answer is final.
type Step struct {
	Answer   string
	ToolCall *ToolCall
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

// Brain runs the loop with a Model and a set of Tools. OnToken, if set, receives
// answer text as it streams from the Model; OnToolCall, if set, is notified of
// each tool call before it runs (a UI hook — tools stay unaware of the UI).
type Brain struct {
	Model       Model
	Tools       map[string]Tool
	MaxSteps    int
	ToolTimeout  time.Duration // per-tool-call deadline; 0 = no limit
	OnToken      func(string)
	OnToolCall   func(ToolCall)                          // before a tool runs
	OnToolResult func(tc ToolCall, out string, err error) // after a tool runs
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
		step, err := b.Model.Next(ctx, conv, b.toolSpecs(), b.OnToken)
		if err != nil {
			return "", conv, err
		}
		if step.ToolCall == nil {
			conv = append(conv, Message{Role: "assistant", Content: step.Answer})
			return step.Answer, conv, nil
		}
		if b.OnToolCall != nil {
			b.OnToolCall(*step.ToolCall)
		}

		conv = append(conv, Message{
			Role:    "assistant",
			Content: "call " + step.ToolCall.Tool + "(" + step.ToolCall.Args + ")",
		})
		result, err := b.invoke(ctx, *step.ToolCall)
		if b.OnToolResult != nil {
			b.OnToolResult(*step.ToolCall, result, err)
		}
		conv = append(conv, Message{Role: "tool", Content: result})
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

// invoke runs a tool call, returning the result or an error string to feed back
// to the Model (an unknown tool or a validation/execution error is reported, not
// fatal, so the Model can correct itself).
func (b *Brain) invoke(ctx context.Context, tc ToolCall) (string, error) {
	tool, ok := b.Tools[tc.Tool]
	if !ok {
		err := errors.New("unknown tool " + tc.Tool)
		return "error: " + err.Error(), err
	}
	if b.ToolTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.ToolTimeout)
		defer cancel()
	}
	out, err := tool.Invoke(ctx, tc.Args)
	if err != nil {
		return "error: " + err.Error(), err
	}
	return out, nil
}

func (b *Brain) toolSpecs() []ToolSpec {
	specs := make([]ToolSpec, 0, len(b.Tools))
	for _, t := range b.Tools {
		specs = append(specs, t.ToolSpec)
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs
}
