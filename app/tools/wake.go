package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
)

// wake is the self-continuation tool: after a delay, the SAME conversation is re-invoked with a note
// as its prompt — for self-paced loops and polling ("re-check the deploy in 5 minutes"). It reaches
// nothing external — it only schedules a future continuation — so it carries zero authority and is
// ungated (like time_now). The runaway-loop risk is bounded instead: the delay is clamped and the
// number of pending wakes is capped. Effects performed in the RESUMED turn still pass the gate + HITL
// normally.
//
// It is id-based, so the chat manager stays wake-agnostic: the session stamps only its chat id onto
// the ctx (WithChatID); wake reads that id and, when the timer fires, resolves the chat through the
// Sessions seam and submits. Resolving by id (not a captured session) means a wake that fires after
// the chat was reaped re-opens it and continues, instead of no-op'ing on a dead session.

// Bounds that guard a runaway self-waking loop. The delay is clamped so a wake can neither hammer
// (too short) nor pin resources forever (too long); the pending cap limits outstanding self-wakes.
const (
	defaultMinDelay   = 1 * time.Second
	defaultMaxDelay   = time.Hour
	defaultMaxPending = 3
)

// ErrTooManyPending is returned when the pending-wake cap is reached.
var ErrTooManyPending = errors.New("wake: too many pending wakes")

// Sessions is the wake tool's lookup seam: resolve a chat by id to its live session. chat.Manager
// satisfies it via its existing Open (which re-opens a reaped chat). Defined here so tools does not
// import chat — chat imports tools for the ctx carrier, not the other way around.
type Sessions interface {
	Open(id string) *agentkit.Session
}

type chatIDKey struct{}

// WithChatID stamps the current chat id onto ctx. The chat manager sets it when it creates a
// session, so a wake scheduled during that chat's turn resumes THAT chat — nothing is captured but
// the id string.
func WithChatID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, chatIDKey{}, id)
}

// chatIDFrom returns the chat id carried by ctx, or "" when there is no chat to resume (a bare
// context) — wake is then unavailable.
func chatIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(chatIDKey{}).(string)
	return id
}

// ChatID returns the chat id carried by ctx, or "" — the exported accessor used for log correlation
// (the diagnostic logger's ctxHandler folds it into every line).
func ChatID(ctx context.Context) string { return chatIDFrom(ctx) }

// Waker schedules self-wakes for whichever chat invoked wake. Min/Max clamp the delay; MaxPending
// caps concurrent pending wakes (zero values fall back to 1s / 1h / 3). It holds the Sessions seam
// (bound after the manager exists, see Bind) and resolves the firing chat by id — so one
// workspace-shared Waker serves every chat.
type Waker struct {
	Min, Max   time.Duration
	MaxPending int

	log      *slog.Logger
	sessions Sessions

	mu     sync.Mutex
	seq    uint64
	timers map[uint64]*time.Timer
}

// WakerOption configures a Waker.
type WakerOption func(*Waker)

// WithWakeLogger sets the diagnostic logger — a wake firing is otherwise invisible. A nil logger is
// ignored (the no-op default stays), so callers never produce a nil logger.
func WithWakeLogger(l *slog.Logger) WakerOption {
	return func(w *Waker) {
		if l != nil {
			w.log = l
		}
	}
}

// NewWaker builds a Waker. Bind must be called (once the manager exists) before a wake can fire; the
// logger defaults to a no-op (never nil), so callers log unconditionally.
func NewWaker(opts ...WakerOption) *Waker {
	w := &Waker{timers: map[uint64]*time.Timer{}, log: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		o(w)
	}
	return w
}

// Bind wires the chat-lookup seam. It is called at workspace assembly, after the manager exists but
// before any turn (so no wake can fire before it is set).
func (w *Waker) Bind(s Sessions) { w.sessions = s }

func (w *Waker) min() time.Duration {
	if w.Min > 0 {
		return w.Min
	}
	return defaultMinDelay
}

func (w *Waker) max() time.Duration {
	if w.Max > 0 {
		return w.Max
	}
	return defaultMaxDelay
}

func (w *Waker) maxPending() int {
	if w.MaxPending > 0 {
		return w.MaxPending
	}
	return defaultMaxPending
}

// Tool exposes wake to the model/scripts. It is ungated (schedules a continuation, reaches nothing
// external); the bounds are the runaway guard.
func (w *Waker) Tool() (agentkit.Tool, error) {
	return agentkit.NewTool("wake",
		"Pause and resume yourself later: after `seconds`, this same conversation is re-invoked with "+
			"`note` as the prompt. Use it to wait then continue — poll something, or re-check after a delay. "+
			"The delay is clamped (min 1s, max 1h). Returns {wakeInSeconds}.",
		w.wake,
		agentkit.WithSchema(agentkit.Object(
			agentkit.Prop("seconds", agentkit.Number("How long to wait before resuming (clamped to 1..3600)")),
			agentkit.Prop("note", agentkit.String(`The prompt to resume with, e.g. "re-check the deploy status"`)),
		).Require("seconds", "note")),
	)
}

func (w *Waker) wake(ctx context.Context, args string) (string, error) {
	var a struct {
		Seconds float64 `json:"seconds"`
		Note    string  `json:"note"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Note == "" {
		return "", errors.New("missing required field: note")
	}
	id := chatIDFrom(ctx)
	if id == "" {
		return "", errors.New("wake is unavailable here (no conversation to resume)")
	}
	delay := w.clamp(time.Duration(a.Seconds * float64(time.Second)))
	if err := w.schedule(delay, id, a.Note); err != nil {
		return "", err
	}
	b, _ := json.Marshal(struct {
		WakeInSeconds float64 `json:"wakeInSeconds"`
	}{delay.Seconds()})
	return string(b), nil
}

// clamp bounds the requested delay into [min, max].
func (w *Waker) clamp(d time.Duration) time.Duration {
	if d < w.min() {
		return w.min()
	}
	if d > w.max() {
		return w.max()
	}
	return d
}

// schedule registers a one-shot timer that resumes chat id with note after delay, unless the pending
// cap is reached.
func (w *Waker) schedule(delay time.Duration, id, note string) error {
	w.mu.Lock()
	if len(w.timers) >= w.maxPending() {
		w.mu.Unlock()
		return ErrTooManyPending
	}
	w.seq++
	tid := w.seq // captured by the closure BEFORE AfterFunc — no self-reference to the timer var
	w.timers[tid] = time.AfterFunc(delay, func() { w.fired(tid, id, note) })
	w.mu.Unlock()
	w.log.Debug("wake scheduled", slog.Duration("delay", delay), slog.String("chat", id))
	return nil
}

// fired drops the timer from the pending set and resumes the chat by id. It runs detached (a timer
// callback, no ctx). A nil seam (never bound) or a nil session (unresolvable chat) is a safe no-op.
func (w *Waker) fired(tid uint64, id, note string) {
	w.mu.Lock()
	delete(w.timers, tid)
	w.mu.Unlock()
	if w.sessions == nil {
		return
	}
	sess := w.sessions.Open(id)
	if sess == nil {
		return
	}
	w.log.Info("wake fired", slog.String("chat", id))
	sess.Submit(note)
}

// Pending reports how many wakes are scheduled but not yet fired.
func (w *Waker) Pending() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.timers)
}

// Cancel stops every pending wake.
func (w *Waker) Cancel() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for tid, t := range w.timers {
		t.Stop()
		delete(w.timers, tid)
	}
}
