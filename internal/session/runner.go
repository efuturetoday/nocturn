package session

import (
	"context"
	"fmt"
	"sync"

	"github.com/efuturetoday/nocturn/internal/activity"
	"github.com/efuturetoday/nocturn/internal/brain"
)

// Runner is the interactive session core: COMMANDS in (Submit/Cancel/Reset),
// EVENTS out (Subscribe), one turn at a time with a buffered input queue. It is the
// headless heart that a TUI, a REST/SSE server, or a mobile app all drive the same
// way — no orchestration lives in any client. A daemon can hold one Runner per
// session (fan-out Subscribe + Snapshot make reconnect/multi-client work).
//
// It drives a Session (the turns it runs, the reset, the history for a Snapshot) via
// the small turns interface — so the queue/loop is testable with a fake, and in
// production a *Session is passed directly (no closure-injection to dodge an import,
// now that both live in this package).
type Runner struct {
	sess turns

	parent context.Context
	cmds   chan command
	done   chan turnResult

	mu         sync.Mutex
	subs       map[int]chan Event
	nextSub    int
	running    bool
	queue      []queuedInput
	cancelTurn context.CancelFunc
	approval   *pendingApproval
	nextAppr   int
}

type queuedInput struct {
	source Source
	input  string
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
	kind   cmdKind
	source Source
	input  string
	id     string // cmdResolve: the approval id
	choice int    // cmdResolve: the chosen option index
}

// pendingApproval is the at-most-one approval a turn is parked on (turns are serial).
// apply(choice) applies the chosen option — an opaque callback the caller supplies, so
// the engine stays free of any approval-mechanism types (the hitl token stays behind it).
type pendingApproval struct {
	event ApprovalEvent
	apply func(choice int)
}

// turns is what a Runner drives: one interactive session's turn execution, reset,
// and history snapshot. *Session satisfies it; a test passes a fake to exercise the
// queue/loop without a model.
type turns interface {
	// Ask runs one turn to a final answer, streaming activity to the sink on ctx.
	Ask(ctx context.Context, input string) (string, error)
	// Reset ends the session and starts fresh (revokes session grants, clears history).
	Reset()
	// History returns the conversation so far, for a Snapshot / reconnecting client.
	History() []brain.Message
}

// NewRunner builds a Runner over sess. Call Start to spin the command/turn loop.
func NewRunner(sess turns) *Runner {
	return &Runner{
		sess: sess,
		cmds: make(chan command, 8),
		done: make(chan turnResult, 1),
		subs: map[int]chan Event{},
	}
}

// Start runs the command/turn loop until ctx is cancelled. Turns derive from ctx, so
// cancelling it stops the loop and any in-flight turn.
func (r *Runner) Start(ctx context.Context) {
	r.parent = ctx
	go r.loop(ctx)
}

// --- commands (the IN port; safe to call from any goroutine / client) ---

// Submit enqueues an input to run as a turn — from the user, or from a wake/remind
// resumption. If a turn is running it is buffered (a QueuedEvent) and runs when the
// current turn ends; otherwise it starts immediately.
func (r *Runner) Submit(source Source, input string) {
	r.cmds <- command{kind: cmdSubmit, source: source, input: input}
}

// Cancel stops the running turn (if any). Buffered inputs remain.
func (r *Runner) Cancel() { r.cmds <- command{kind: cmdCancel} }

// Reset ends the session and starts fresh: cancels a running turn, drops the queue,
// resets the underlying session.
func (r *Runner) Reset() { r.cmds <- command{kind: cmdReset} }

// Resolve answers the pending approval `id` with option index `choice`. A no-op if
// no such approval is pending (already answered, or answered out of band).
func (r *Runner) Resolve(id string, choice int) {
	r.cmds <- command{kind: cmdResolve, id: id, choice: choice}
}

// PresentApproval surfaces an approval request on the event stream and remembers how
// to apply it. intent + labels are client-facing; apply(choice) is the caller's
// opaque callback that enacts the chosen option (e.g. resolving a hitl token — that
// specificity lives in the caller, not here). It returns immediately: the wait for an
// answer, its ttl and cancellation, belong to whoever issued the request.
func (r *Runner) PresentApproval(intent string, labels []string, apply func(choice int)) {
	r.mu.Lock()
	r.nextAppr++
	ev := ApprovalEvent{ID: fmt.Sprintf("appr-%d", r.nextAppr), Intent: intent, Options: labels}
	r.approval = &pendingApproval{event: ev, apply: apply}
	r.mu.Unlock()
	r.emit(ev)
}

// onStreamEvent is the single activity sink installed on each turn's ctx (see
// begin): the model adapter's tokens/thinking and the Registry's tool events all
// arrive here and fan out as Runner events. One seam replaces the former separate
// TokenSink (brain.OnToken) and ToolSink (registry.OnCall) wiring.
func (r *Runner) onStreamEvent(e activity.Event) {
	switch ev := e.(type) {
	case activity.Token:
		r.emit(TokenEvent{Text: ev.Text})
	case activity.Thinking:
		r.emit(ThinkingEvent{Text: ev.Text})
	case activity.ToolEvent:
		r.emit(ToolEvent{Event: ev})
	}
}

// Notice emits a system line (e.g. a background scheduler message routed to this session).
func (r *Runner) Notice(text string, isErr bool) { r.emit(NoticeEvent{Text: text, Err: isErr}) }

// --- subscription + snapshot (the OUT port) ---

// Subscribe returns a live event channel and an unsubscribe function. Multiple
// subscribers fan out (a TUI and a phone on the same session; a reconnecting client).
// A subscriber that falls behind drops events and should re-Snapshot to resync.
func (r *Runner) Subscribe() (<-chan Event, func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch := make(chan Event, 256)
	id := r.nextSub
	r.nextSub++
	r.subs[id] = ch
	return ch, func() {
		r.mu.Lock()
		if _, ok := r.subs[id]; ok {
			delete(r.subs, id)
			close(ch)
		}
		r.mu.Unlock()
	}
}

// Snapshot is the state a late-joining or reconnecting client needs: the conversation
// so far, whether a turn is running, and the buffered queue.
type Snapshot struct {
	Running  bool
	Queue    []QueuedItem
	Messages []brain.Message
	Pending  *ApprovalEvent // an approval awaiting an answer, or nil
}

// QueuedItem is one buffered input in a Snapshot.
type QueuedItem struct {
	Input  string
	Source Source
}

// Snapshot returns the current session state. (Message history reflects completed
// turns; the live turn's tokens arrive via the event stream.)
func (r *Runner) Snapshot() Snapshot {
	r.mu.Lock()
	s := Snapshot{Running: r.running, Queue: make([]QueuedItem, len(r.queue))}
	for i, q := range r.queue {
		s.Queue[i] = QueuedItem{Input: q.input, Source: q.source}
	}
	if r.approval != nil {
		ev := r.approval.event
		s.Pending = &ev
	}
	r.mu.Unlock()
	s.Messages = r.sess.History()
	return s
}

// --- the loop (single owner of turn sequencing) ---

func (r *Runner) loop(ctx context.Context) {
	for {
		select {
		case c := <-r.cmds:
			switch c.kind {
			case cmdSubmit:
				r.onSubmit(c.source, c.input)
			case cmdCancel:
				r.onCancel()
			case cmdReset:
				r.onReset()
			case cmdResolve:
				r.onResolve(c.id, c.choice)
			}
		case res := <-r.done:
			r.onTurnDone(res)
		case <-ctx.Done():
			return
		}
	}
}

func (r *Runner) onSubmit(src Source, input string) {
	r.mu.Lock()
	running := r.running
	if running {
		r.queue = append(r.queue, queuedInput{src, input})
	}
	r.mu.Unlock()
	if running {
		r.emit(QueuedEvent{Input: input, Source: src})
		return
	}
	r.begin(queuedInput{src, input})
}

func (r *Runner) begin(qi queuedInput) {
	turnCtx, cancel := context.WithCancel(r.parent)
	turnCtx = WithApprovalSink(turnCtx, r)                // attended approvals surface on THIS session's stream
	turnCtx = activity.WithSink(turnCtx, r.onStreamEvent) // tokens/thinking/tool events fan out to subscribers
	r.mu.Lock()
	r.running = true
	r.cancelTurn = cancel
	r.mu.Unlock()
	r.emit(TurnStartEvent{Input: qi.input, Source: qi.source})
	go func() {
		ans, err := r.sess.Ask(turnCtx, qi.input)
		r.done <- turnResult{ans, err}
	}()
}

func (r *Runner) onTurnDone(res turnResult) {
	r.mu.Lock()
	r.running = false
	r.cancelTurn = nil
	var next *queuedInput
	if len(r.queue) > 0 {
		q := r.queue[0]
		r.queue = r.queue[1:]
		next = &q
	}
	r.mu.Unlock()
	r.emit(TurnEndEvent{Answer: res.answer, Err: res.err})
	if next != nil {
		r.begin(*next)
	}
}

func (r *Runner) onResolve(id string, choice int) {
	r.mu.Lock()
	pa := r.approval
	ok := pa != nil && pa.event.ID == id
	if ok {
		r.approval = nil
	}
	r.mu.Unlock()
	if !ok {
		return // no such pending approval (already answered, or out of band)
	}
	if pa.apply != nil {
		pa.apply(choice) // enacts the decision; the parked turn unblocks wherever it waits
	}
	r.emit(ApprovalResolvedEvent{ID: id})
}

func (r *Runner) onCancel() {
	r.mu.Lock()
	c := r.cancelTurn
	r.mu.Unlock()
	if c != nil {
		c()
	}
}

func (r *Runner) onReset() {
	r.mu.Lock()
	c := r.cancelTurn
	r.queue = nil
	r.approval = nil // a parked approval is abandoned; the cancelled turn denies in the engine
	r.mu.Unlock()
	if c != nil {
		c()
	}
	r.sess.Reset()
	r.emit(NoticeEvent{Text: "new session"})
}

// emit fans an event out to every subscriber, dropping for any that is behind (it
// must re-Snapshot to resync — a slow client never stalls the loop or other clients).
func (r *Runner) emit(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ch := range r.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
