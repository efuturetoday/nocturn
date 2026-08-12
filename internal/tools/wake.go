package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"
	"sync/atomic"
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

// Wake is one scheduled self-continuation, persisted so it survives a restart.
//
// It is persisted for the same reason a Reminder is, and the absence of that was a silent loss: a
// wake lived only in a time.AfterFunc, so a restart — or a workspace being closed and reopened —
// dropped every outstanding continuation with no log line and no error. The model had arranged to
// come back in ten minutes and simply never did, which reads as the model forgetting rather than as
// state being thrown away.
type Wake struct {
	ID     string    `json:"id"`
	FireAt time.Time `json:"fireAt"`
	ChatID string    `json:"chatId"`
	Note   string    `json:"note"`
}

// Waker schedules self-wakes for whichever chat invoked wake. Min/Max clamp the delay; MaxPending
// caps concurrent pending wakes (zero values fall back to 1s / 1h / 3). It holds the Sessions seam
// (bound after the manager exists, see Bind) and resolves the firing chat by id — so one
// workspace-shared Waker serves every chat.
//
// Its store is a control-plane file OUTSIDE the model's file mount, exactly like the reminder store.
type Waker struct {
	Min, Max   time.Duration
	MaxPending int

	path     string
	log      *slog.Logger
	sessions Sessions

	mu     sync.Mutex
	items  map[string]Wake
	timers map[string]*time.Timer
	seq    atomic.Uint64
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

// WithWakeStore persists pending wakes at path. An unset path is in-memory only (tests), which is
// the same bargain NewReminders offers.
func WithWakeStore(path string) WakerOption {
	return func(w *Waker) { w.path = path }
}

// NewWaker builds a Waker, reading any persisted wakes (tolerantly — a missing or malformed file is
// an empty store). Call Restore once Bind has run to arm them; Bind must be called before a wake can
// fire. The logger defaults to a no-op (never nil), so callers log unconditionally.
func NewWaker(opts ...WakerOption) *Waker {
	w := &Waker{
		items:  map[string]Wake{},
		timers: map[string]*time.Timer{},
		log:    slog.New(slog.DiscardHandler),
	}
	for _, o := range opts {
		o(w)
	}
	w.load()
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

// schedule persists a wake and arms its timer, unless the pending cap is reached.
func (w *Waker) schedule(delay time.Duration, id, note string) error {
	wk := Wake{
		ID:     fmt.Sprintf("wake-%d-%d", time.Now().UnixNano(), w.seq.Add(1)),
		FireAt: time.Now().Add(delay),
		ChatID: id,
		Note:   note,
	}
	w.mu.Lock()
	if len(w.items) >= w.maxPending() {
		w.mu.Unlock()
		return ErrTooManyPending
	}
	w.items[wk.ID] = wk
	w.save()
	w.enroll(wk)
	w.mu.Unlock()
	w.log.Debug("wake scheduled", slog.Duration("delay", delay), slog.String("chat", id))
	return nil
}

// enroll arms a timer for one wake. Callers hold w.mu.
func (w *Waker) enroll(wk Wake) {
	if t, ok := w.timers[wk.ID]; ok {
		t.Stop()
	}
	delay := max(time.Until(wk.FireAt), 0)
	id := wk.ID
	w.timers[id] = time.AfterFunc(delay, func() { w.fired(id) })
}

// Restore arms every persisted wake; one that came due while the process was down fires promptly
// (the delay is clamped to ≥0), the same catch-up Reminders.Restore does.
//
// Call it AFTER Bind. An overdue wake fires as soon as its timer is armed, and firing consumes the
// wake — so arming before the lookup seam exists would drop exactly the wakes this persistence was
// built to keep.
func (w *Waker) Restore() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, wk := range w.items {
		w.enroll(wk)
	}
}

// fired consumes the wake and resumes its chat by id. It runs detached (a timer callback, no ctx).
func (w *Waker) fired(id string) {
	w.mu.Lock()
	wk, ok := w.items[id]
	if ok {
		if t := w.timers[id]; t != nil {
			t.Stop()
		}
		delete(w.timers, id)
		delete(w.items, id)
		w.save()
	}
	w.mu.Unlock()
	if !ok {
		return
	}
	// A wake is one-shot and is already out of the store, so a resume that cannot happen is lost. It
	// runs on a timer with no caller to return to — say so, or the one thing this tool exists for
	// fails with no trace at all.
	if w.sessions == nil {
		w.log.Warn("wake fired but no session seam is bound — the continuation is lost", "chat", wk.ChatID)
		return
	}
	sess := w.sessions.Open(wk.ChatID)
	if sess == nil {
		w.log.Warn("wake fired but its chat could not be opened — the continuation is lost", "chat", wk.ChatID)
		return
	}
	w.log.Info("wake fired", slog.String("chat", wk.ChatID))
	sess.Submit(wk.Note)
}

// Pending reports how many wakes are scheduled but not yet fired.
func (w *Waker) Pending() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.items)
}

// Cancel stops every pending timer, leaving the wakes persisted to be re-armed by the next Restore —
// the same shutdown semantics as Reminders.Cancel.
func (w *Waker) Cancel() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for id, t := range w.timers {
		t.Stop()
		delete(w.timers, id)
	}
}

// load reads the persisted wakes (tolerant: missing/malformed → empty). Called once at construction.
func (w *Waker) load() {
	if w.path == "" {
		return
	}
	data, err := os.ReadFile(w.path)
	if err != nil {
		return
	}
	var list []Wake
	if err := json.Unmarshal(data, &list); err != nil {
		return
	}
	for _, wk := range list {
		w.items[wk.ID] = wk
	}
}

// save persists the pending set atomically (write then rename), 0600. Callers hold w.mu.
func (w *Waker) save() {
	if w.path == "" {
		return
	}
	list := make([]Wake, 0, len(w.items))
	for _, wk := range w.items {
		list = append(list, wk)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].FireAt.Before(list[j].FireAt) })
	// Every failure is logged rather than swallowed. What is being lost here is the record that
	// survives a restart, so a silent failure means the model's self-continuation simply never
	// happens — and that is precisely the failure mode this file was written to close.
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		w.log.Error("wake: encoding the pending wakes failed", "err", err)
		return
	}
	tmp := w.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		w.log.Error("wake: writing the pending wakes failed", "path", tmp, "err", err)
		return
	}
	if err := os.Rename(tmp, w.path); err != nil {
		w.log.Error("wake: replacing the pending wakes failed", "path", w.path, "err", err)
	}
}
