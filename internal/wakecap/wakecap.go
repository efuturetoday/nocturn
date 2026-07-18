// Package wakecap is the self-wake capability: wake — the running agent schedules
// its OWN re-invocation after a delay, with the session's context preserved. It is
// the ephemeral, in-process half of scheduling (the persistent reminder is separate):
// "wait N seconds, then continue with this note" — for self-paced loops and polling
// ("check the deploy again in 5 minutes").
//
// wake reaches NOTHING external — it only schedules a future continuation — so it
// carries zero authority and is NOT broker-gated (like time.now). The risk is a
// runaway self-waking loop, so it is BOUNDED instead: the delay is clamped and the
// number of pending wakes is capped. Effects performed in the RESUMED turn still pass
// the broker + HITL normally.
//
// It is ephemeral: pending wakes live only as long as the process (and the session).
// Cancel() drops them — the session calls it on Reset/Close so a dead conversation's
// wakes never fire.
package wakecap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/internal/tool"
)

// Bounds that guard a runaway self-waking loop (overridable per Waker). The delay is
// clamped so a wake can neither hammer (too short) nor pin resources forever (too
// long); the pending cap limits how many self-wakes can be outstanding at once.
const (
	defaultMinDelay   = 1 * time.Second // just enough to stop a tight resume loop; "in 2s" is legit
	defaultMaxDelay   = time.Hour
	defaultMaxPending = 3
)

// ErrTooManyPending is returned when the pending-wake cap is reached.
var ErrTooManyPending = errors.New("wakecap: too many pending wakes")

// Resume starts a fresh turn on the caller's OWN session with note as its input. It must be
// routed so it serializes with normal turns (not called re-entrantly mid-turn) — wake fires
// it LATER, after the current turn ended. It is carried on the turn ctx (WithResume) so the
// workspace-shared wake tool resumes whichever chat invoked it, not a single ambient one.
type Resume func(note string)

type resumeKey struct{}

// WithResume stamps the caller's resume onto ctx. A runner sets this per turn (its
// context decorator) so a wake scheduled during the turn resumes THAT runner's chat.
func WithResume(ctx context.Context, r Resume) context.Context {
	return context.WithValue(ctx, resumeKey{}, r)
}

// ResumeFrom returns the resume carried by ctx, or nil when the caller has no session to
// resume (a bare context) — wake is then unavailable and returns an error.
func ResumeFrom(ctx context.Context) Resume {
	r, _ := ctx.Value(resumeKey{}).(Resume)
	return r
}

// Waker schedules self-wakes. Min/Max clamp the delay; MaxPending caps concurrent pending
// wakes. Zero values fall back to sane defaults (1s / 1h / 3). It holds no target — each
// wake captures its resume from the calling turn's ctx, so one workspace-shared Waker
// serves every chat.
type Waker struct {
	Min, Max   time.Duration
	MaxPending int

	mu     sync.Mutex
	seq    uint64                 // next pending-timer id
	timers map[uint64]*time.Timer // keyed by id, so a firing closure never self-references its own timer
}

// New builds a Waker. A wake's target comes from the calling turn's ctx (WithResume).
func New() *Waker {
	return &Waker{timers: map[uint64]*time.Timer{}}
}

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

// Tool exposes wake to the model/scripts. It is ungated (schedules a continuation,
// reaches nothing external); the bounds below are the runaway guard.
func (w *Waker) Tool() tool.Tool {
	return tool.Tool{
		Spec: tool.Spec{
			Name: "wake",
			Description: "Pause and resume yourself later: after `seconds`, this same conversation is re-invoked " +
				"with `note` as the prompt. Use it to wait then continue — poll something, or re-check after a delay. " +
				"The delay is clamped (min 1s, max 1h). Returns {wakeInSeconds}.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"seconds":{"type":"number","description":"How long to wait before resuming (clamped to 1..3600)"},` +
				`"note":{"type":"string","description":"The prompt to resume with, e.g. \"re-check the deploy status\""}` +
				`},"required":["seconds","note"]}`),
		},
		Invoke: func(ctx context.Context, args string) (string, error) {
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
			resume := ResumeFrom(ctx)
			if resume == nil {
				return "", errors.New("wake is unavailable here (no conversation to resume)")
			}
			delay := w.clamp(time.Duration(a.Seconds * float64(time.Second)))
			if err := w.schedule(delay, a.Note, resume); err != nil {
				return "", err
			}
			b, _ := json.Marshal(struct {
				WakeInSeconds float64 `json:"wakeInSeconds"`
			}{delay.Seconds()})
			return string(b), nil
		},
	}
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

// schedule registers a one-shot timer that fires resume(note) after delay, unless the
// pending cap is reached. resume is captured from the calling turn's ctx, so the timer
// resumes the right chat even after that turn (and its ctx) is gone.
func (w *Waker) schedule(delay time.Duration, note string, resume Resume) error {
	w.mu.Lock()
	if len(w.timers) >= w.maxPending() {
		w.mu.Unlock()
		return ErrTooManyPending
	}
	w.seq++
	id := w.seq // captured by the closure BEFORE AfterFunc — no self-reference to the timer var
	w.timers[id] = time.AfterFunc(delay, func() { w.fired(id, note, resume) })
	w.mu.Unlock()
	return nil
}

// fired removes the timer from the pending set and resumes.
func (w *Waker) fired(id uint64, note string, resume Resume) {
	w.mu.Lock()
	delete(w.timers, id)
	w.mu.Unlock()
	resume(note)
}

// Pending reports how many wakes are scheduled but not yet fired.
func (w *Waker) Pending() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.timers)
}

// Cancel stops every pending wake — the session calls this on Reset/Close so a dead
// conversation's wakes never resume it.
func (w *Waker) Cancel() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for id, t := range w.timers {
		t.Stop()
		delete(w.timers, id)
	}
}
