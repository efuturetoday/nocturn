package agent

import (
	"context"
	"sync"

	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// Runner is the interactive session core: COMMANDS in (Submit/Cancel/Reset),
// EVENTS out (Subscribe), one turn at a time with a buffered input queue. It is the
// headless heart that a TUI, a REST/SSE server, or a mobile app all drive the same
// way — no orchestration lives in any client. A daemon can hold one Runner per
// session (fan-out Subscribe + Snapshot make reconnect/multi-client work).
//
// How a turn actually runs is injected (run) so the queue/loop can be tested without
// a model; the composition passes Session.Ask wired to this Runner's token/tool sinks.
type Runner struct {
	run     func(ctx context.Context, input string) (string, error)
	reset   func()
	history func() []brain.Message // conversation snapshot; may be nil

	parent context.Context
	cmds   chan command
	done   chan turnResult

	mu         sync.Mutex
	subs       map[int]chan Event
	nextSub    int
	running    bool
	queue      []queuedInput
	cancelTurn context.CancelFunc
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
)

type command struct {
	kind   cmdKind
	source Source
	input  string
}

// NewRunner builds a Runner. run executes one turn (streaming via the token/tool
// sinks); reset starts a fresh session; history returns the conversation for a
// Snapshot (any may be nil in tests). Call Start to spin the loop.
func NewRunner(run func(ctx context.Context, input string) (string, error), reset func(), history func() []brain.Message) *Runner {
	return &Runner{
		run:     run,
		reset:   reset,
		history: history,
		cmds:    make(chan command, 8),
		done:    make(chan turnResult, 1),
		subs:    map[int]chan Event{},
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
func (r *Runner) Submit(source Source, input string) { r.cmds <- command{cmdSubmit, source, input} }

// Cancel stops the running turn (if any). Buffered inputs remain.
func (r *Runner) Cancel() { r.cmds <- command{kind: cmdCancel} }

// Reset ends the session and starts fresh: cancels a running turn, drops the queue,
// resets the underlying session.
func (r *Runner) Reset() { r.cmds <- command{kind: cmdReset} }

// TokenSink / ToolSink are wired into the per-session brain (OnToken) and registry
// view (OnCall) so streamed tokens and tool events fan out as Runner events.
func (r *Runner) TokenSink() func(string) { return func(t string) { r.emit(TokenEvent{Text: t}) } }
func (r *Runner) ToolSink() func(tool.Event) {
	return func(ev tool.Event) { r.emit(ToolEvent{Event: ev}) }
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
	r.mu.Unlock()
	if r.history != nil {
		s.Messages = r.history()
	}
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
	r.mu.Lock()
	r.running = true
	r.cancelTurn = cancel
	r.mu.Unlock()
	r.emit(TurnStartEvent{Input: qi.input, Source: qi.source})
	go func() {
		ans, err := r.run(turnCtx, qi.input)
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
	r.mu.Unlock()
	if c != nil {
		c()
	}
	if r.reset != nil {
		r.reset()
	}
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
