package chat

import (
	"context"
	"fmt"

	"github.com/efuturetoday/nocturn/internal/activity"
	"github.com/efuturetoday/nocturn/internal/brain"
)

// This file is the chat's serialized turn loop: COMMANDS in (Submit/Cancel/Reset/
// Resolve), EVENTS out (Subscribe/Tap), one turn at a time over a buffered input
// queue. It is the headless heart that a TUI, a REST/SSE server, or a mobile app
// all drive the same way — no orchestration lives in any client. A daemon holds
// one Chat per conversation (fan-out Subscribe + Snapshot make reconnect and
// multi-client work).

type queuedInput struct {
	source  Source
	display string       // client-facing line (typed "/skill …"); == input for a plain message
	input   string       // what actually runs (an expanded skill body)
	agent   string       // non-empty: spawn this named agent with input as its task (via the WithAgents resolver)
	effort  brain.Effort // per-message reasoning override for this turn; "" = the charter default
}

type turnResult struct {
	answer string
	err    error
}

type cmdKind int

const (
	cmdSubmit cmdKind = iota
	cmdCancel
	cmdReset
	cmdResolve
)

type command struct {
	kind    cmdKind
	source  Source
	display string
	input   string
	agent   string
	effort  brain.Effort // cmdSubmit: per-message reasoning override
	id      string       // cmdResolve: the approval id
	choice  int          // cmdResolve: the chosen option index
}

// pendingApproval is the at-most-one approval a turn is parked on (turns are serial).
// apply(choice) applies the chosen option — an opaque callback the caller supplies, so
// the engine stays free of any approval-mechanism types (the hitl token stays behind it).
type pendingApproval struct {
	event ApprovalEvent
	apply func(choice int)
}

// Start runs the command/turn loop until ctx is cancelled. Turns derive from ctx, so
// cancelling it stops the loop and any in-flight turn.
func (c *Chat) Start(ctx context.Context) {
	c.parent = ctx
	go c.loop(ctx)
}

// --- commands (the IN port; safe to call from any goroutine / client) ---

// send hands a command to the loop, aborting instead of blocking when the chat is
// closed (quit closed → the loop is gone; a late wake/submit against a reaped
// chat must never wedge its caller).
func (c *Chat) send(cmd command) {
	select {
	case c.cmds <- cmd:
	case <-c.quit:
	}
}

// Submit enqueues an input to run as a turn — from the user, or from a wake/remind
// resumption. If a turn is running it is buffered (a QueuedEvent) and runs when the
// current turn ends; otherwise it starts immediately. Display equals input. effort is a
// per-message reasoning override for THIS turn ("" = the chat's charter default).
func (c *Chat) Submit(source Source, input string, effort brain.Effort) {
	c.send(command{kind: cmdSubmit, source: source, display: input, input: input, effort: effort})
}

// SubmitInput is Submit with a distinct client-facing display line (input is what runs;
// display is what a client shows) — for a slash-skill whose expanded body must not be
// echoed into the transcript. effort is the per-message reasoning override.
func (c *Chat) SubmitInput(source Source, display, input string, effort brain.Effort) {
	c.send(command{kind: cmdSubmit, source: source, display: display, input: input, effort: effort})
}

// SubmitAgent enqueues a named child-agent run (resolved via the WithAgents charter
// resolver, run as a one-shot Once) as a turn on this same serialized loop, so it
// streams and gates exactly like a chat turn. display is the client-facing line;
// task is the agent's input.
func (c *Chat) SubmitAgent(display, name, task string) {
	c.send(command{kind: cmdSubmit, source: SourceSpawn, display: display, input: task, agent: name})
}

// Cancel stops the running turn (if any). Buffered inputs remain.
func (c *Chat) Cancel() { c.send(command{kind: cmdCancel}) }

// Reset starts the chat over: cancels a running turn, drops the queue, revokes the
// scope and clears the history (see resetState).
func (c *Chat) Reset() { c.send(command{kind: cmdReset}) }

// Resolve answers the pending approval `id` with option index `choice`. A no-op if
// no such approval is pending (already answered, or answered out of band).
func (c *Chat) Resolve(id string, choice int) {
	c.send(command{kind: cmdResolve, id: id, choice: choice})
}

// PresentApproval surfaces an approval request on the event stream and remembers how
// to apply it. intent + labels are client-facing; apply(choice) is the caller's
// opaque callback that enacts the chosen option (e.g. resolving a hitl token — that
// specificity lives in the caller, not here). It returns immediately: the wait for an
// answer, its ttl and cancellation, belong to whoever issued the request.
func (c *Chat) PresentApproval(intent string, labels []string, apply func(choice int)) {
	c.mu.Lock()
	c.nextAppr++
	ev := ApprovalEvent{ID: fmt.Sprintf("appr-%d", c.nextAppr), Intent: intent, Options: labels}
	c.approval = &pendingApproval{event: ev, apply: apply}
	c.mu.Unlock()
	c.emit(ev)
}

// onStreamEvent is the single activity sink installed on each turn's ctx (see
// begin): the model adapter's tokens/thinking and the Registry's tool events all
// arrive here and fan out as chat events.
func (c *Chat) onStreamEvent(e activity.Event) {
	switch ev := e.(type) {
	case activity.Token:
		c.emit(TokenEvent{Text: ev.Text})
	case activity.Thinking:
		c.emit(ThinkingEvent{Text: ev.Text})
	case activity.ToolEvent:
		c.recordFrame(ev)
		c.emit(ToolEvent{Event: ev})
	}
}

// recordFrame accumulates the durable tool forest: a Start opens a frame (id/parent/tool/
// args), the matching End fills its result/err. Guarded by mu (the turn goroutine calls
// this concurrently with the loop). Runs BEFORE emit so a snapshot taken right after an End
// already reflects the completed frame.
func (c *Chat) recordFrame(ev activity.ToolEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ev.Phase == activity.Start {
		c.forestIx[ev.ID] = len(c.forest)
		c.forest = append(c.forest, ToolFrame{ID: ev.ID, Parent: ev.Parent, Tool: ev.Tool, Args: ev.Args})
		return
	}
	if i, ok := c.forestIx[ev.ID]; ok { // End: fill the outcome onto the opened frame
		c.forest[i].Result = ev.Result
		c.forest[i].Err = errText(ev.Err)
	}
}

// Notice emits a system line (e.g. a background scheduler message routed to this chat).
func (c *Chat) Notice(text string, isErr bool) { c.emit(NoticeEvent{Text: text, Err: isErr}) }

// --- subscription + snapshot (the OUT port) ---

// Subscribe returns a live event channel and an unsubscribe function. Multiple
// subscribers fan out (a TUI and a phone on the same chat; a reconnecting client).
// A subscriber that falls behind drops events and should re-Snapshot to resync.
func (c *Chat) Subscribe() (<-chan Event, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan Event, 256)
	id := c.nextID
	c.nextID++
	c.subs[id] = ch
	return ch, func() {
		c.mu.Lock()
		if _, ok := c.subs[id]; ok {
			delete(c.subs, id)
			close(ch)
		}
		c.mu.Unlock()
	}
}

// Tap registers a PASSIVE observer of the event stream: it fans out exactly like a
// Subscribe, but does NOT count as a watching client (see HasClients). The persistence
// pump taps — it must see every turn to save/badge, yet its presence must never make an
// unwatched chat look attended (which would keep an approval off the phone). unsub closes
// the channel so the tap's reader exits.
func (c *Chat) Tap() (<-chan Event, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan Event, 256)
	id := c.nextID
	c.nextID++
	c.taps[id] = ch
	return ch, func() {
		c.mu.Lock()
		if _, ok := c.taps[id]; ok {
			delete(c.taps, id)
			close(ch)
		}
		c.mu.Unlock()
	}
}

// idle reports whether the chat has nothing running, nothing queued, and no command
// still buffered for the loop — the Manager's reap condition for a finished one-shot.
// The buffered-command check matters: a Deliver serialized before the reap may have
// handed the loop an input it has not yet dequeued.
func (c *Chat) idle() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.running && len(c.queue) == 0 && len(c.cmds) == 0
}

// HasClients reports whether a REAL client is watching the stream right now (a tap does
// not count). The approval router reads this at Ask-time to decide whether a human is
// reachable in-band, or the request must also go out-of-band to the phone.
func (c *Chat) HasClients() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.subs) > 0
}

// ClearPending drops the parked approval (if any) and announces it resolved. It is called
// when a decision arrives through ANY channel — including out of band, where the chat's
// own Resolve never ran — so a reconnecting client's Snapshot never shows a phantom prompt.
// Idempotent: if nothing is parked (already resolved in-band), it is a no-op.
func (c *Chat) ClearPending() {
	c.mu.Lock()
	if c.approval == nil {
		c.mu.Unlock()
		return
	}
	id := c.approval.event.ID
	c.approval = nil
	c.mu.Unlock()
	c.emit(ApprovalResolvedEvent{ID: id})
}

// Snapshot is the state a late-joining or reconnecting client needs: the conversation
// so far, whether a turn is running, and the buffered queue.
type Snapshot struct {
	Running  bool
	Queue    []QueuedItem
	Messages []brain.Message
	Forest   []ToolFrame    // the completed tool call tree (sub-calls + errors), for reload
	Pending  *ApprovalEvent // an approval awaiting an answer, or nil
}

// QueuedItem is one buffered input in a Snapshot.
type QueuedItem struct {
	Display string
	Input   string
	Source  Source
}

// Snapshot returns the current chat state. (Message history reflects completed
// turns; the live turn's tokens arrive via the event stream.)
func (c *Chat) Snapshot() Snapshot {
	c.mu.Lock()
	s := Snapshot{Running: c.running, Queue: make([]QueuedItem, len(c.queue))}
	for i, q := range c.queue {
		s.Queue[i] = QueuedItem{Display: q.display, Input: q.input, Source: q.source}
	}
	if c.approval != nil {
		ev := c.approval.event
		s.Pending = &ev
	}
	s.Forest = append([]ToolFrame(nil), c.forest...) // copy under the lock
	conv := c.conv
	c.mu.Unlock()
	s.Messages = conv.Messages()
	return s
}

// --- the loop (single owner of turn sequencing) ---

func (c *Chat) loop(ctx context.Context) {
	for {
		select {
		case cmd := <-c.cmds:
			switch cmd.kind {
			case cmdSubmit:
				c.onSubmit(queuedInput{source: cmd.source, display: cmd.display, input: cmd.input, agent: cmd.agent, effort: cmd.effort})
			case cmdCancel:
				c.onCancel()
			case cmdReset:
				c.onReset()
			case cmdResolve:
				c.onResolve(cmd.id, cmd.choice)
			}
		case res := <-c.done:
			c.onTurnDone(res)
		case <-c.quit: // Close ran (reap/Delete/CloseAll) — the loop must not outlive the chat
			return
		case <-ctx.Done():
			return
		}
	}
}

func (c *Chat) onSubmit(qi queuedInput) {
	c.mu.Lock()
	running := c.running
	if running {
		c.queue = append(c.queue, qi)
	}
	c.mu.Unlock()
	if running {
		c.emit(QueuedEvent{Display: qi.display, Input: qi.input, Source: qi.source})
		return
	}
	c.begin(qi)
}

// begin builds the ORCHESTRATION ctx for one turn — cancel, the activity sink, the
// approval sink, the per-chat decorator — and spins the turn goroutine. The
// PERMISSION ctx (scope bind, skills, budget) is built inside turn (see chat.go);
// the split keeps one writer per concern.
func (c *Chat) begin(qi queuedInput) {
	turnCtx, cancel := context.WithCancel(c.parent)
	turnCtx = activity.WithSink(turnCtx, c.onStreamEvent) // tokens/thinking/tool events fan out to subscribers
	if c.decorate != nil {
		turnCtx = c.decorate(turnCtx) // chat-scoped identity (e.g. this chat's wake target)
	}
	// ALWAYS carry the approval sink: the approval is recorded on this chat (so a
	// reconnecting client sees it in the snapshot) whether or not a client is watching now.
	// Whether it ALSO goes out-of-band is decided at Ask-time by the router (HasClients) —
	// not latched here at turn start, because a long background turn can gain or lose a
	// client between here and the Ask.
	turnCtx = WithApprovalSink(turnCtx, c)
	// Reasoning effort for THIS turn: the per-message override wins, else the chat's charter
	// default (unset "" → the model adapter's global default). Survives scope.Bind to model.Next.
	// NOTE: an in-chat /agent spawn (qi.agent != "") runs under its OWN charter via Once but
	// inherits THIS chat's effort here — a per-spawn override is deferred (SubmitAgent takes none).
	// A SCHEDULED agent run is unaffected: its chat's charter IS the agent charter.
	eff := qi.effort
	if eff == "" {
		eff = c.charter.Effort
	}
	turnCtx = brain.WithEffort(turnCtx, eff)
	c.mu.Lock()
	c.running = true
	c.cancelTurn = cancel
	// Capture the turn's state HERE (begin runs on the loop goroutine, serialized
	// with onReset) — see turnState in chat.go.
	st := turnState{conv: c.conv, scope: c.scope, skills: c.skills}
	c.mu.Unlock()
	c.emit(TurnStartEvent{Display: qi.display, Input: qi.input, Source: qi.source})
	go func() {
		ans, err := c.runTurn(turnCtx, st, qi)
		c.done <- turnResult{ans, err}
	}()
}

// runTurn executes one queued item: a named agent spawn (its charter resolved via
// WithAgents, run as a throwaway Once under the parent turn's ctx — so it streams
// and asks into this chat) or, by default, a chat turn against st. An agent
// submission with no resolver wired, or an unknown agent name, is a turn-level
// error, not a panic — the loop surfaces it as a TurnEndEvent.
func (c *Chat) runTurn(ctx context.Context, st turnState, qi queuedInput) (string, error) {
	if qi.agent != "" {
		if c.agents == nil {
			return "", fmt.Errorf("agent runs not supported by this chat")
		}
		ch, err := c.agents(qi.agent)
		if err != nil {
			return "", err
		}
		return Once(ctx, c.engine, c.guard, ch, qi.input)
	}
	return c.turn(ctx, st, qi.input)
}

func (c *Chat) onTurnDone(res turnResult) {
	c.mu.Lock()
	c.cancelTurn = nil
	var next *queuedInput
	if len(c.queue) > 0 {
		q := c.queue[0]
		c.queue = c.queue[1:]
		next = &q
	}
	// With a queued input the chat transitions DIRECTLY into the next turn: running
	// stays true, so an observer (the reaping pump) never sees a false idle between
	// TurnEnd and the next TurnStart.
	if next == nil {
		c.running = false
	}
	c.mu.Unlock()
	c.emit(TurnEndEvent{Answer: res.answer, Err: res.err})
	if next != nil {
		c.begin(*next)
	}
}

func (c *Chat) onResolve(id string, choice int) {
	c.mu.Lock()
	pa := c.approval
	ok := pa != nil && pa.event.ID == id
	if ok {
		c.approval = nil
	}
	c.mu.Unlock()
	if !ok {
		return // no such pending approval (already answered, or out of band)
	}
	if pa.apply != nil {
		pa.apply(choice) // enacts the decision; the parked turn unblocks wherever it waits
	}
	c.emit(ApprovalResolvedEvent{ID: id})
}

func (c *Chat) onCancel() {
	c.mu.Lock()
	cancel := c.cancelTurn
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *Chat) onReset() {
	c.mu.Lock()
	cancel := c.cancelTurn
	c.queue = nil
	c.approval = nil // a parked approval is abandoned; the cancelled turn denies in the engine
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.resetState()
	c.emit(NoticeEvent{Text: "new session"})
}

// emit fans an event out to every subscriber AND every passive tap, dropping for any
// that is behind (it must re-Snapshot to resync — a slow client never stalls the loop or
// other clients). Taps (the persistence pump) receive the same stream but never affect
// attendance.
func (c *Chat) emit(e Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ch := range c.subs {
		select {
		case ch <- e:
		default:
		}
	}
	for _, ch := range c.taps {
		select {
		case ch <- e:
		default:
		}
	}
}
