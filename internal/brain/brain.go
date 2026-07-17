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

// Message is one turn in the conversation. Role is "system", "user", "assistant",
// or "tool". The optional leading "system" turn carries the standing instructions
// (an interactive session's persona, or a child agent's own Instructions) — seeded
// once by NewConversation, never appended to a user turn. An assistant turn that
// calls tools carries them in ToolCalls (native tool_calls, not text); a tool result
// carries the id of the call it answers in ToolCallID, so results match their calls
// by id — not by position.
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
// one turn; run executes them CONCURRENTLY (see run), but gated effects still
// serialize at the human (the notifier is serialized), so every approval stays a
// deliberate, single decision.
type Step struct {
	Answer    string
	ToolCalls []ToolCall
}

// Model produces the next Step given the conversation and the available tools.
// The Model streams its output — answer text and reasoning — to the activity sink
// carried by ctx (activity.Emit); a run with no sink on ctx is simply silent. The
// Model therefore needs no output parameter: streaming is a ctx-carried
// cross-cutting concern, not part of its result.
type Model interface {
	Next(ctx context.Context, conv []Message, tools []tool.Spec) (Step, error)
}

// Brain runs the loop with a Model. It is IMMUTABLE, stateless, and shared: it holds
// no per-run state at all — the toolset a turn may use is passed IN (to Run /
// Conversation), and streaming + tool observation travel on ctx via internal/activity.
// So one Brain serves every conversation and every agent run without being copied to
// vary a toolset or a sink. The tools it is handed vary per run (an agent gets a
// filtered Registry); the Brain never owns them.
type Brain struct {
	model       Model
	maxSteps    int
	toolTimeout time.Duration // per-tool-call deadline; 0 = no limit
}

// Option configures a Brain built with New.
type Option func(*Brain)

// WithMaxSteps caps how many loop iterations a run may take before ErrMaxSteps.
func WithMaxSteps(n int) Option { return func(b *Brain) { b.maxSteps = n } }

// WithToolTimeout sets the per-tool-call deadline (0 = no limit).
func WithToolTimeout(d time.Duration) Option { return func(b *Brain) { b.toolTimeout = d } }

// New builds a Brain over model (the LLM port) with the given options. It is the ONLY
// way to construct a Brain — the fields are private, so there is no second, literal
// path to keep in sync.
func New(model Model, opts ...Option) *Brain {
	b := &Brain{model: model}
	for _, o := range opts {
		o(b)
	}
	return b
}

const defaultMaxSteps = 8

// maxToolOutput bounds how much of a tool's result is fed back to the Model, to
// keep the context bounded. It is applied HERE — the single point where a tool
// result reaches the model — not per tool, and deliberately NOT in the Registry:
// the Registry also serves the script interpreter, and a script's nocturn.call
// results are processed programmatically and must not be truncated.
const maxToolOutput = 4000

// run drives the loop over conv until the Model gives a final answer or the step
// budget is exhausted, appending every turn (assistant/tool/final answer) to conv
// and returning the answer plus the extended conversation. tools is the toolset this
// run may use — passed in, not held, so the Brain stays stateless and shared.
func (b *Brain) run(ctx context.Context, conv []Message, tools *tool.Registry) (string, []Message, error) {
	steps := b.maxSteps
	if steps <= 0 {
		steps = defaultMaxSteps
	}

	for i := 0; i < steps; i++ {
		step, err := b.model.Next(ctx, conv, tools.Specs())
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
			wg.Go(func() { results[idx] = b.invoke(ctx, tc, tools) })
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
	tools *tool.Registry // the toolset this conversation runs against (the session's)
	conv  []Message
}

// ConvOption configures a Conversation built with NewConversation.
type ConvOption func(*Conversation)

// WithSystem seeds the leading role=system message — the standing instruction (a
// session's persona, or a child agent's Instructions). Optional: an empty string (or
// omitting the option) starts a conversation with no system turn. This is the ONE place
// the system role is set — the model adapter transports it, never invents it.
func WithSystem(system string) ConvOption {
	return func(c *Conversation) {
		if system != "" {
			c.conv = append(c.conv, Message{Role: "system", Content: system})
		}
	}
}

// WithHistory seeds prior conversation turns (a saved chat being reopened) after the
// system message. It skips any role=system message in the restored history — the current
// persona is provided fresh by WithSystem, so a chat reopened after a persona change picks
// up the new one rather than the one it was saved with.
func WithHistory(msgs []Message) ConvOption {
	return func(c *Conversation) {
		for _, m := range msgs {
			if m.Role != "system" {
				c.conv = append(c.conv, m)
			}
		}
	}
}

// NewConversation starts a conversation on this Brain over tools — the toolset every
// turn in it may use (an interactive session's shared Registry). Options seed the
// system turn (WithSystem) and any restored history (WithHistory); with none it starts empty.
func (b *Brain) NewConversation(tools *tool.Registry, opts ...ConvOption) *Conversation {
	c := &Conversation{brain: b, tools: tools}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Messages returns a copy of the conversation history so far — for a client snapshot
// (a reconnecting/late-joining UI). It is a snapshot: call it between turns; the live
// turn's tokens arrive via streaming, not here.
func (c *Conversation) Messages() []Message { return append([]Message(nil), c.conv...) }

// Send adds the user's input, runs the loop to a final answer, and keeps the
// whole exchange in the conversation's history.
func (c *Conversation) Send(ctx context.Context, input string) (string, error) {
	c.conv = append(c.conv, Message{Role: "user", Content: input})
	ans, conv, err := c.brain.run(ctx, c.conv, c.tools)
	c.conv = conv
	return ans, err
}

// invoke runs a tool call through tools (the run's Registry, which emits the observer
// events) and returns the content to feed back to the Model — the tool's output, or
// an "error: ..." string on failure (an unknown tool or a validation/execution error
// is reported, not fatal, so the Model can correct itself).
func (b *Brain) invoke(ctx context.Context, tc ToolCall, tools *tool.Registry) string {
	if b.toolTimeout > 0 {
		var cancel context.CancelFunc
		// A pausable budget so an out-of-band approval inside the tool doesn't
		// burn the per-tool deadline (hitl pauses it during the human wait).
		ctx, cancel = deadline.WithBudget(ctx, b.toolTimeout)
		defer cancel()
	}
	out, err := tools.Invoke(ctx, tc.Tool, tc.Args)
	if err != nil {
		return truncate("error: "+timeoutCause(ctx, err).Error(), maxToolOutput)
	}
	// Most results are bounded at maxToolOutput to keep the context small; a tool
	// whose output is durable instruction text (a skill body) may raise its own
	// budget via Spec.MaxResult so it is not silently corrupted by truncation.
	budget := maxToolOutput
	if m := tools.MaxResult(tc.Tool); m > maxToolOutput {
		budget = m
	}
	return truncate(out, budget)
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
