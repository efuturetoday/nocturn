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
	"errors"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/internal/deadline"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// ErrMaxSteps is returned when the loop hits its step budget without the Model
// producing a final answer.
var ErrMaxSteps = errors.New("brain: max steps exceeded without a final answer")

// Message is one turn in the conversation. Role is "user", "assistant", or
// "tool". An assistant turn that calls tools carries them in ToolCalls (native
// tool_calls, not text); a tool result carries the id of the call it answers in
// ToolCallID, so results match their calls by id — not by position.
type Message struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall // assistant turn: the tool calls it requested
	ToolCallID string     // tool turn: the id of the call this result answers
}

// ToolCall is the Model's request to invoke a tool with JSON arguments. ID is the
// tool_call_id that ties the call to its result across the conversation (the
// model supplies it; the adapter synthesizes one if the endpoint omits it).
type ToolCall struct {
	ID   string
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

// Model produces the next Step given the conversation and the available tools.
// If onToken is non-nil, the Model streams answer text through it as it arrives
// (tool-call decisions do not stream). onToken may be nil for no streaming.
type Model interface {
	Next(ctx context.Context, conv []Message, tools []tool.Spec, onToken func(string)) (Step, error)
}

// Brain runs the loop with a Model and a shared Registry of tools. OnToken, if
// set, receives answer text as it streams from the Model. Tool-call observation
// lives on the Registry — which both the Brain and the script interpreter
// dispatch through — so every tool call, model- or script-issued, is seen in one
// place; the Brain itself carries no per-tool UI hook.
type Brain struct {
	Model       Model
	Registry    *tool.Registry
	MaxSteps    int
	ToolTimeout time.Duration // per-tool-call deadline; 0 = no limit
	OnToken     func(string)  // answer-token stream; nil = no streaming
}

const defaultMaxSteps = 8

// maxToolOutput bounds how much of a tool's result is fed back to the Model, to
// keep the context bounded. It is applied HERE — the single point where a tool
// result reaches the model — not per tool, and deliberately NOT in the Registry:
// the Registry also serves the script interpreter, and a script's nocturn.call
// results are processed programmatically and must not be truncated.
const maxToolOutput = 4000

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
		// One native assistant turn carrying all requested calls, then a role=tool
		// result per call keyed by its id. The calls run CONCURRENTLY (each gets its
		// own deadline budget and goes through the shared, thread-safe Registry); a
		// failing/denied call never aborts the others — every result is fed back. The
		// results are stitched back in CALL ORDER, so the history is deterministic no
		// matter which call finishes first. Gated effects still serialize at the
		// human (the notifier is serialized), so only auto-allowed ones truly overlap.
		conv = append(conv, Message{Role: "assistant", ToolCalls: step.ToolCalls})
		results := make([]string, len(step.ToolCalls))
		var wg sync.WaitGroup
		for idx, tc := range step.ToolCalls {
			wg.Go(func() { results[idx] = b.invoke(ctx, tc) })
		}
		wg.Wait()
		for idx, tc := range step.ToolCalls {
			conv = append(conv, Message{Role: "tool", ToolCallID: tc.ID, Content: results[idx]})
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
		return truncate("error: "+timeoutCause(ctx, err).Error(), maxToolOutput)
	}
	return truncate(out, maxToolOutput)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…(truncated)"
	}
	return s
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
