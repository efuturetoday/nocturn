package agentkit

import (
	"context"
	"errors"
	"time"
)

// Stop reasons a turn can end with, surfaced on TurnEnd.Err (a wall-clock stop uses the ctx's
// context.DeadlineExceeded).
var (
	ErrMaxSteps   = errors.New("agentkit: max steps reached")
	ErrTokenLimit = errors.New("agentkit: token limit reached")
	ErrMaxDepth   = errors.New("agentkit: max sub-agent depth reached")
	ErrMaxSpawns  = errors.New("agentkit: max sub-agent spawns reached")
)

// Session is a live conversation: a serialized turn loop driven by Submit (input in) and
// observed via Subscribe (event stream out). It holds the running history.
type Session struct {
	llm        LLM
	tools      ToolSet
	skills     SkillSet
	system     string
	timeout    time.Duration
	tokenLimit int
	effort     Effort
	maxSteps   int
	maxDepth   int // sub-agent nesting depth cap (top-level only; nested runs inherit)
	maxSpawns  int // total sub-agent population cap across the whole tree (top-level only)
	log        Logger
	store      Store              // nil = in-memory only, no persistence
	id         string             // transcript key in store
	cancel     context.CancelFunc // stops the internal turn loop (Close); ctx itself is NOT stored
	// history []Message, in/out channels — TODO
}

// Option configures a Session.
type Option func(*Session)

func WithTools(t ToolSet) Option      { panic("TODO") }
func WithSkills(s SkillSet) Option    { panic("TODO") }
func WithSystem(system string) Option { panic("TODO") }
func WithEffort(e Effort) Option      { panic("TODO") }
func WithLogger(l Logger) Option      { panic("TODO") }

// WithStore persists this session's transcript under id: history is loaded from the store when
// the session is built and saved after each turn. Without it a session is in-memory only.
// Multiplexing many sessions and evicting idle ones is the CONSUMER's concern (its own
// map[id]*Session + policy) — the library owns no live-session lifecycle.
func WithStore(store Store, id string) Option { panic("TODO") }

// The three runaway-loop guards — one per stop dimension, named unambiguously (no bare
// "Budget"): rounds, wall-clock, tokens. Any turn stops on whichever trips first.

// WithMaxSteps caps model round-trips per turn (0 = a sensible default).
func WithMaxSteps(n int) Option { panic("TODO") }

// WithTimeout caps wall-clock per turn. It is PAUSABLE — time spent waiting on out-of-band
// approval does not count (see withTimeout). 0 = none.
func WithTimeout(d time.Duration) Option { panic("TODO") }

// WithTokenLimit caps the turn's cumulative BILLED tokens (sum of every round-trip's prompt +
// completion). Reactive: the loop checks the running total after each round-trip and stops with
// ErrTokenLimit before the next. 0 = none. No tokenizer needed — the count comes from the
// provider response. (max_tokens, the per-response output cap, is a separate adapter option.)
func WithTokenLimit(n int) Option { panic("TODO") }

// Sub-agent spawn guards — bound a nested AgentTool tree. Depth alone is insufficient (a
// depth-capped tree can still fan out), so cap both, and the inherited shared token/time budget
// caps the tree's total cost regardless of shape. Set at the top level; nested runs inherit.

// WithMaxDepth caps how deep AgentTool spawns may nest (0 = a sensible default). A spawn past it is
// refused with ErrMaxDepth, surfaced to the model as the tool result.
func WithMaxDepth(n int) Option { panic("TODO") }

// WithMaxSpawns caps the TOTAL number of sub-agents spawned across the whole tree (0 = a sensible
// default) — the population guard depth cannot provide. Exceeding it refuses with ErrMaxSpawns.
func WithMaxSpawns(n int) Option { panic("TODO") }

// NewSession builds a Session and starts its internal turn loop under ctx. ctx is the session's
// LIFECYCLE: cancelling it (or Close) stops the loop, cancels any in-flight turn, and closes the
// Subscribe channel. Each Submit'd turn runs under a child of ctx decorated with the event sink,
// the per-turn timeout and fresh guards. Without WithLogger it uses NopLogger().
func NewSession(ctx context.Context, llm LLM, opts ...Option) *Session { panic("TODO") }

// Submit enqueues a user turn (serialized: turns run one at a time under the session ctx).
func (s *Session) Submit(input string) { /* TODO */ }

// Subscribe returns the output stream: answer/reasoning tokens, tool events, turn lifecycle. It
// closes when the session ctx is cancelled or Close is called.
func (s *Session) Subscribe() <-chan Event { panic("TODO") }

// Close stops the session: it cancels the internal loop and any in-flight turn and closes the
// Subscribe channel. Sugar for cancelling the ctx passed to NewSession; safe to call more than once.
func (s *Session) Close() { /* TODO */ }

// run drives the agentic loop for one turn over conv: ask the LLM, and while it returns tool
// calls, run them (in PARALLEL) through the tool set and feed the results back, until it returns a
// final answer or a guard trips. It reads its config from the Session's own fields (llm, tools,
// maxSteps, tokenLimit, timeout, effort) rather than a parameter list. It accumulates each
// round-trip's Step.Tokens; if tokenLimit > 0 and the running total reaches it, it stops with
// ErrTokenLimit before the next round-trip.
//
// Invariants the loop must hold:
//   - native tool_call_id association (results matched to calls by id, not position),
//   - parallel tool execution within a turn,
//   - a shared call-instance id counter (ctx) for the nested tool event forest,
//   - a pausable wall-clock timeout (an HITL wait inside a tool must not consume it),
//   - ToolStart/ToolEnd emitted on the event sink around each Call.
func (s *Session) run(ctx context.Context, conv []Message) (string, error) {
	panic("TODO")
}

// Once runs a throwaway session for a single input and returns its final answer — the synchronous
// convenience over Submit/Subscribe (events still stream to the ctx sink if one is attached). It
// is the primitive an agent firing or a subagent tool builds on. Pass WithStore to persist the
// one-shot transcript.
func Once(ctx context.Context, llm LLM, input string, opts ...Option) (string, error) {
	panic("TODO")
}
