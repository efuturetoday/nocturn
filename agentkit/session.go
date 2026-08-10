package agentkit

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"
)

// Stop reasons a turn can end with, surfaced on TurnEnd.Err (a wall-clock stop uses the ctx's
// context.DeadlineExceeded).
var (
	ErrMaxSteps    = errors.New("agentkit: max steps reached")
	ErrTokenLimit  = errors.New("agentkit: token limit reached")
	ErrMaxDepth    = errors.New("agentkit: max sub-agent depth reached")
	ErrMaxSpawns   = errors.New("agentkit: max sub-agent spawns reached")
	ErrTurnTimeout = errors.New("agentkit: turn timed out")
)

// Session is a live conversation: a serialized turn loop driven by Submit (input in) and observed
// via Subscribe (event stream out). It holds the running history.
type Session struct {
	llm LLM
	// What the model is given, each supplied by a function evaluated ONCE PER TURN rather than held
	// as a value. A turn is the right granularity for all three: the tool list goes to the provider at
	// turn start and the model plans against it, so it must not shift underneath a turn in progress —
	// but a consumer whose world changed between two turns (a skill installed, an MCP server
	// connected) should be answered by the very next one, not by the next session.
	//
	// Only the function form is stored, and the value forms below are sugar over it. Holding both a
	// value and a function would mean a precedence rule — "which one wins" — for no gain: a constant
	// is a function that returns the same thing.
	toolsFn    func() ToolSet
	skillsFn   func() SkillSet
	systemFn   func() string
	timeout    time.Duration
	tokenLimit int
	tokenizer  Tokenizer // optional: estimate Step.Tokens when the provider reports none
	effort     Effort
	maxSteps   int
	maxDepth   int // sub-agent nesting depth cap (top-level only; nested runs inherit)
	maxSpawns  int // total sub-agent population cap across the whole tree (top-level only)
	log        Logger
	store      Store              // nil = in-memory only, no persistence
	id         string             // transcript key in store
	cancel     context.CancelFunc // stops the internal turn loop (Close); ctx itself is NOT stored

	in   chan string
	out  chan Event
	done chan struct{}

	mu         sync.Mutex
	history    []Message
	turnCancel context.CancelFunc // cancels the in-flight turn (Cancel); nil when idle
}

// Option configures a Session.
type Option func(*Session)

// WithToolsFunc supplies the toolset through a function evaluated once per turn, for a consumer
// whose available tools change over a session's lifetime — an extension installed or removed while
// the conversation is open. The turn boundary is what makes it safe: a turn is handed its set once
// and works with it throughout, so a tool can never vanish between two calls the model already
// planned together, and the next turn simply sees the new world.
func WithToolsFunc(fn func() ToolSet) Option { return func(s *Session) { s.toolsFn = fn } }

// WithSkillsFunc is WithToolsFunc for the skill catalog, evaluated on the same boundary.
func WithSkillsFunc(fn func() SkillSet) Option { return func(s *Session) { s.skillsFn = fn } }

// WithSystemFunc supplies the system prompt through a function evaluated once per turn, for a prompt
// whose content can change over a session's lifetime (a consumer folding in mutable context). Safe
// because the system message is ephemeral — assemble prepends it to the durable history rather than
// storing it — so a changed prompt cannot corrupt a persisted transcript.
//
// All three funcs must be safe for concurrent use only insofar as the caller shares one across
// sessions; a single session evaluates it on its own turn goroutine.
func WithSystemFunc(fn func() string) Option { return func(s *Session) { s.systemFn = fn } }

// The value forms, sugar over the funcs above: a constant is a function that returns the same thing
// every turn.
func WithTools(t ToolSet) Option    { return WithToolsFunc(func() ToolSet { return t }) }
func WithSkills(sk SkillSet) Option { return WithSkillsFunc(func() SkillSet { return sk }) }
func WithSystem(v string) Option    { return WithSystemFunc(func() string { return v }) }
func WithEffort(e Effort) Option    { return func(s *Session) { s.effort = e } }

func WithLogger(l Logger) Option {
	return func(s *Session) {
		if l != nil {
			s.log = l
		}
	}
}

// WithStore persists this session's transcript under id: history is loaded from the store when the
// session is built and saved after each turn. Without it a session is in-memory only. Multiplexing
// many sessions and evicting idle ones is the CONSUMER's concern (its own map[id]*Session + policy)
// — the library owns no live-session lifecycle.
func WithStore(store Store, id string) Option {
	return func(s *Session) { s.store, s.id = store, id }
}

// The three runaway-loop guards — one per stop dimension, named unambiguously (no bare "Budget"):
// rounds, wall-clock, tokens. Any turn stops on whichever trips first.

// WithMaxSteps caps model round-trips per turn (0 = a sensible default).
func WithMaxSteps(n int) Option { return func(s *Session) { s.maxSteps = n } }

// WithTimeout caps wall-clock per turn. It is PAUSABLE — time spent waiting on out-of-band approval
// does not count (see withTimeout). 0 = none.
func WithTimeout(d time.Duration) Option { return func(s *Session) { s.timeout = d } }

// WithTokenLimit caps the turn's cumulative BILLED tokens (sum of every round-trip's prompt +
// completion). Reactive: the loop checks the running total after each round-trip and stops with
// ErrTokenLimit before the next. 0 = none. The count comes from the provider response; if the
// provider omits it, configure WithTokenizer to fall back to an estimate. (max_tokens, the
// per-response output cap, is a separate adapter option.)
func WithTokenLimit(n int) Option { return func(s *Session) { s.tokenLimit = n } }

// WithTokenizer sets a fallback tokenizer to ESTIMATE a round-trip's tokens at the model boundary
// (the conversation sent in, the step returned out) when the provider reports no usage — so
// WithTokenLimit and the [tokens] readout still work against endpoints that omit it. When the
// provider returns usage, that exact count wins and the tokenizer is unused. Estimation is
// approximate; exact billing is the provider's usage.
func WithTokenizer(t Tokenizer) Option { return func(s *Session) { s.tokenizer = t } }

// Sub-agent spawn guards — bound a nested AgentTool tree. Depth alone is insufficient (a
// depth-capped tree can still fan out), so cap both, and the inherited shared token/time budget caps
// the tree's total cost regardless of shape. Set at the top level; nested runs inherit.

// WithMaxDepth caps how deep AgentTool spawns may nest (0 = a sensible default). A spawn past it is
// refused with ErrMaxDepth, surfaced to the model as the tool result.
func WithMaxDepth(n int) Option { return func(s *Session) { s.maxDepth = n } }

// WithMaxSpawns caps the TOTAL number of sub-agents spawned across the whole tree (0 = a sensible
// default) — the population guard depth cannot provide. Exceeding it refuses with ErrMaxSpawns.
func WithMaxSpawns(n int) Option { return func(s *Session) { s.maxSpawns = n } }

// buildSession applies options and loads persisted history, without starting the async loop.
func buildSession(llm LLM, opts ...Option) *Session {
	s := &Session{llm: llm, log: NopLogger(), maxSteps: defaultMaxSteps}
	for _, o := range opts {
		o(s)
	}
	if s.store != nil && s.id != "" {
		if h, err := s.store.Load(s.id); err != nil {
			s.log.Warn("agentkit: load history failed", "id", s.id, "err", err)
		} else {
			s.history = h
		}
	}
	return s
}

// NewSession builds a Session and starts its internal turn loop under ctx. ctx is the session's
// LIFECYCLE: cancelling it (or Close) stops the loop, cancels any in-flight turn, and closes the
// Subscribe channel. Each Submit'd turn runs under a child of ctx decorated with the event sink, the
// per-turn timeout and fresh guards. Without WithLogger it uses NopLogger().
func NewSession(ctx context.Context, llm LLM, opts ...Option) *Session {
	s := buildSession(llm, opts...)
	s.in = make(chan string, 16)
	s.out = make(chan Event, 64)
	s.done = make(chan struct{})
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	go s.loop(runCtx)
	return s
}

// Submit enqueues a user turn (serialized: turns run one at a time under the session ctx). It blocks
// only until the input is queued, or returns immediately if the session has stopped.
func (s *Session) Submit(input string) {
	select {
	case s.in <- input:
	case <-s.done:
	}
}

// Subscribe returns the output stream: answer/reasoning tokens, tool events, turn lifecycle. It
// closes when the session ctx is cancelled or Close is called. Intended for a single consumer.
func (s *Session) Subscribe() <-chan Event { return s.out }

// Close stops the session: it cancels the internal loop and any in-flight turn and closes the
// Subscribe channel. Sugar for cancelling the ctx passed to NewSession; safe to call more than once.
func (s *Session) Close() {
	if s.cancel != nil {
		s.cancel()
	}
}

// Cancel aborts the in-flight turn (if any) without stopping the session: the turn ends with a
// context.Canceled error, its partial output is still appended and persisted, and the next Submit
// runs normally. A no-op when no turn is running.
func (s *Session) Cancel() {
	s.mu.Lock()
	cancel := s.turnCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Session) setTurnCancel(cancel context.CancelFunc) {
	s.mu.Lock()
	s.turnCancel = cancel
	s.mu.Unlock()
}

// loop is the session's single worker: it processes submitted inputs one at a time until ctx is
// cancelled, then closes the output stream (it is the sole sender) and signals done.
func (s *Session) loop(ctx context.Context) {
	defer close(s.out)
	defer close(s.done)
	for {
		select {
		case <-ctx.Done():
			return
		case input := <-s.in:
			s.turn(ctx, input)
		}
	}
}

// turn runs one full turn: it decorates ctx with the event sink and guards, drives the loop, appends
// the produced messages to history, persists, and brackets the whole thing with TurnStart/TurnEnd.
func (s *Session) turn(ctx context.Context, input string) {
	ctx = WithSink(ctx, s.sink(ctx))
	ctx, cancelTurn := context.WithCancel(ctx)
	s.setTurnCancel(cancelTurn)
	defer func() {
		s.setTurnCancel(nil)
		cancelTurn()
	}()
	ctx, cancel := s.decorate(ctx)
	defer cancel()

	Emit(ctx, TurnStart{})
	s.appendMsgs(Message{Role: RoleUser, Content: input})
	v := s.view()
	_, produced, total, err := s.run(ctx, v.tools, s.assemble(v))
	// A wall-clock deadline cancels ctx with cause ErrTurnTimeout — surface that clear reason instead
	// of the bare "context canceled" the aborted model/tool call bubbled up.
	if context.Cause(ctx) == ErrTurnTimeout {
		err = ErrTurnTimeout
	}
	s.appendMsgs(produced...)
	s.persist()
	Emit(ctx, TurnEnd{Err: err, Tokens: total})
}

// sink returns the event sink for a turn: it forwards to the output stream but never blocks past
// cancellation.
func (s *Session) sink(ctx context.Context) func(Event) {
	return func(e Event) {
		select {
		case s.out <- e:
		case <-ctx.Done():
		}
	}
}

// decorate installs the per-run ctx state shared by the async turn and Once: the call-id counter,
// the token budget, the spawn limits, the reasoning effort and the pausable timeout. Each is
// inherit-if-present, so an embedded sub-agent run reuses the outer session's counter, budget, spawn
// pool and remaining time rather than starting fresh. It does NOT install the event sink (the async
// turn does that; Once keeps the caller's sink so a sub-agent streams to the parent).
func (s *Session) decorate(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx = withCounter(ctx)
	ctx = withPausedClock(ctx)
	ctx = withTokenBudget(ctx, s.tokenLimit)
	ctx = withSpawnLimits(ctx, s.maxDepth, s.maxSpawns)
	ctx = withEffort(ctx, s.effort)
	return withTimeout(ctx, s.timeout)
}

// run drives the agentic loop for one turn over conv: ask the LLM, and while it returns tool calls,
// run them (in PARALLEL) through the tool set and feed the results back, until it returns a final
// answer or a guard trips. It returns the final answer, the durable messages produced this turn
// (assistant tool-call messages, tool results, final answer), and the accumulated TokenCount.
func (s *Session) run(ctx context.Context, tools ToolSet, conv []Message) (answer string, produced []Message, total TokenCount, err error) {
	steps := s.maxSteps
	if steps <= 0 {
		steps = defaultMaxSteps
	}
	specs := tools.Specs()
	log := s.log.WithContext(ctx).With("component", "turn")
	depth, _ := ctx.Value(spawnDepthKey{}).(int) // 0 = top-level; >0 = sub-agent
	log.Info("turn start", "depth", depth, "tools", len(specs))
	var used int
	defer func() {
		// A clean answer is Info; any other stop (max steps/tokens, timeout, model error) is Warn so an
		// error turn pops in the log without agentkit re-logging the returned error itself.
		lvl := log.Info
		if err != nil {
			lvl = log.Warn
		}
		lvl("turn end", "stop", stopReason(err), "steps", used, "tokens", total.Total, "depth", depth)
	}()
	for i := 0; i < steps; i++ {
		if e := ctx.Err(); e != nil {
			return answer, produced, total, e
		}
		used = i + 1
		step, e := s.llm.Next(ctx, conv, specs)
		if e != nil {
			// Return, don't also log: the consumer handles it once (scheduler logs a fired agent's
			// failure; an interactive turn's error rides TurnEnd to the client + the manager). The
			// turn-end breadcrumb below records the stop category regardless.
			return answer, produced, total, fmt.Errorf("agentkit: model call: %w", e)
		}
		if step.Tokens.Total == 0 && s.tokenizer != nil {
			step.Tokens = s.estimate(ctx, conv, step)
		}
		total.add(step.Tokens)
		log.Debug("step", "n", used, "tool_calls", len(step.ToolCalls), "tokens", step.Tokens.Total)

		if len(step.ToolCalls) == 0 {
			answer = step.Answer
			final := Message{Role: RoleAssistant, Content: step.Answer}
			produced = append(produced, final)
			if spend(ctx, step.Tokens.Total) {
				return answer, produced, total, ErrTokenLimit
			}
			return answer, produced, total, nil
		}

		asst := Message{Role: RoleAssistant, Content: step.Answer, ToolCalls: step.ToolCalls}
		conv = append(conv, asst)
		produced = append(produced, asst)

		results := s.runTools(ctx, tools, step.ToolCalls)
		conv = append(conv, results...)
		produced = append(produced, results...)

		if spend(ctx, step.Tokens.Total) {
			return answer, produced, total, ErrTokenLimit
		}
	}
	return answer, produced, total, ErrMaxSteps
}

// runTools executes a turn's tool calls concurrently and returns their results as role=tool messages
// in call order. A tool error is not fatal — it becomes the tool result so the model can adjust.
func (s *Session) runTools(ctx context.Context, tools ToolSet, calls []ToolCall) []Message {
	log := s.log.WithContext(ctx).With("component", "tool")
	results := make([]Message, len(calls))
	var wg sync.WaitGroup
	for i, tc := range calls {
		wg.Go(func() {
			log.Debug("tool dispatch", "tool", tc.Tool, "args_len", len(tc.Args))
			start := time.Now()
			pausedStart := pausedNanos(ctx)
			out, err := tools.Call(ctx, tc.Tool, tc.Args)
			if err != nil {
				log.Warn("tool failed", "tool", tc.Tool, "err", err)
				out = "error: " + err.Error()
			}
			// Persist the call's ACTIVE wall-clock (excluding any out-of-band approval wait) so the
			// duration survives a reload (the live stream has it on ToolEnd; a reopened transcript reads
			// it from here). Without the subtraction a call that parked on an approval would report the
			// human's decision time as its own runtime.
			results[i] = Message{Role: RoleTool, ToolCallID: tc.ID, Content: out, DurationMs: activeSince(ctx, start, pausedStart).Milliseconds()}
		})
	}
	wg.Wait()
	return results
}

// stopReason renders a turn's terminating error as a short, greppable log token (not the raw error
// string), so "turn end" lines aggregate cleanly by outcome.
func stopReason(err error) string {
	switch {
	case err == nil:
		return "answer"
	case errors.Is(err, ErrMaxSteps):
		return "max_steps"
	case errors.Is(err, ErrTokenLimit):
		return "token_limit"
	case errors.Is(err, ErrMaxDepth):
		return "max_depth"
	case errors.Is(err, ErrMaxSpawns):
		return "max_spawns"
	case errors.Is(err, ErrTurnTimeout), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "error"
	}
}

// turnView is what ONE turn was handed: the tools the model may call, the skills it may load, and
// the system prompt naming them. All three are resolved together, once, at the top of a turn.
//
// Together is the point. The skill catalog goes into the system prompt and skill_load goes into the
// toolset; resolving them at two moments would let a turn advertise a skill its load tool no longer
// knows, or the reverse. One resolution per turn makes that unrepresentable.
type turnView struct {
	tools  ToolSet
	skills SkillSet
	system string
}

// view resolves the per-turn inputs. Callers take it once and pass it down.
func (s *Session) view() turnView {
	v := turnView{}
	if s.toolsFn != nil {
		v.tools = s.toolsFn()
	}
	if s.skillsFn != nil {
		v.skills = s.skillsFn()
	}
	if s.systemFn != nil {
		v.system = s.systemFn()
	}
	if len(v.skills) > 0 {
		// The tools the model sees are the session's plus skill_load, which pulls a skill body into
		// context on demand. Merged into a copy: v.tools came from the consumer and stays untouched.
		merged := make(ToolSet, len(v.tools)+1)
		maps.Copy(merged, v.tools)
		lt := v.skills.LoadTool()
		merged[lt.Spec().Name] = lt
		v.tools = merged
	}
	return v
}

// assemble builds the messages sent to the model: the (ephemeral) system prompt and skills catalog,
// followed by the durable history.
func (s *Session) assemble(v turnView) []Message {
	var conv []Message
	if sys := v.systemPrompt(); sys != "" {
		conv = append(conv, Message{Role: RoleSystem, Content: sys})
	}
	s.mu.Lock()
	conv = append(conv, s.history...)
	s.mu.Unlock()
	return conv
}

func (v turnView) systemPrompt() string {
	sys := v.system
	if len(v.skills) > 0 {
		sys += "\n\n" + skillsCatalog(v.skills)
	}
	return strings.TrimSpace(sys)
}

func skillsCatalog(skills SkillSet) string {
	var b strings.Builder
	b.WriteString("Available skills — call ")
	b.WriteString(loadSkillToolName)
	b.WriteString(" with a name to load one's full instructions:\n")
	for _, k := range skills.Specs() {
		fmt.Fprintf(&b, "- %s: %s\n", k.Name, k.Description)
	}
	return b.String()
}

func (s *Session) appendMsgs(msgs ...Message) {
	if len(msgs) == 0 {
		return
	}
	s.mu.Lock()
	s.history = append(s.history, msgs...)
	s.mu.Unlock()
}

func (s *Session) persist() {
	if s.store == nil || s.id == "" {
		return
	}
	s.mu.Lock()
	h := make([]Message, len(s.history))
	copy(h, s.history)
	s.mu.Unlock()
	if err := s.store.Save(s.id, h); err != nil {
		s.log.Warn("agentkit: persist failed", "id", s.id, "err", err)
	}
}

// estimate approximates a round-trip's tokens at the model boundary via the configured Tokenizer:
// the whole conversation sent in as prompt, the step's answer and tool-call arguments as completion.
// A tokenizer error is logged and that piece counts as zero — a rough estimate beats failing.
func (s *Session) estimate(ctx context.Context, conv []Message, step Step) TokenCount {
	count := func(text string) int {
		if text == "" {
			return 0
		}
		n, err := s.tokenizer.Count(text)
		if err != nil {
			s.log.WithContext(ctx).Warn("agentkit: tokenizer count failed", "err", err)
			return 0
		}
		return n
	}
	prompt := 0
	for _, m := range conv {
		prompt += count(m.Content)
		for _, tc := range m.ToolCalls {
			prompt += count(tc.Args)
		}
	}
	completion := count(step.Answer)
	for _, tc := range step.ToolCalls {
		completion += count(tc.Args)
	}
	return TokenCount{Prompt: prompt, Completion: completion, Total: prompt + completion}
}

// Once runs a throwaway session for a single input and returns its final answer — the synchronous
// convenience over Submit/Subscribe (events still stream to the ctx sink if one is attached). It is
// the primitive an agent firing or a sub-agent tool builds on. Because decorate is inherit-if-present
// and Once keeps the caller's sink, a sub-agent's Once inherits the parent's budget, spawn pool and
// call-id counter and streams to the parent's event sink. Pass WithStore to persist the transcript.
func Once(ctx context.Context, llm LLM, input string, opts ...Option) (string, error) {
	s := buildSession(llm, opts...)
	ctx, cancel := s.decorate(ctx)
	defer cancel()

	Emit(ctx, TurnStart{})
	s.appendMsgs(Message{Role: RoleUser, Content: input})
	v := s.view()
	answer, produced, total, err := s.run(ctx, v.tools, s.assemble(v))
	s.appendMsgs(produced...)
	s.persist()
	Emit(ctx, TurnEnd{Err: err, Tokens: total})
	return answer, err
}
