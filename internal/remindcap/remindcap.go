// Package remindcap is the reminder capability: remind — "tell me at time T about
// X". It is the persistent, decoupled half of scheduling (the ephemeral self-wake
// lives in wakecap): a reminder is captured now, survives restart, and fires a plain
// notification at its time — NO model run, so it carries almost no authority.
//
// Timing is its OWN concern here — one time.AfterFunc per pending reminder, so it
// fires precisely at its time and depends on nothing else (symmetric with wakecap).
// It is deliberately NOT bolted onto the agent scheduler, which fires agent RUNS
// (autonomy/overlap), a different responsibility.
//
// Security shape (mirrors notify, plus persistence):
//   - the message is LEAK-SCANNED on create (fail fast) and again at fire;
//   - the reminder is stored in the CONTROL-PLANE (reminders.json, outside the
//     model's mount) so the model can neither see nor file.write it — the only way
//     to add/cancel one is this gated tool (load-bearing like grants.json, ADR-10);
//   - it runs silently (Write:false) — a benign future notice needs no per-reminder
//     approval — but still passes the Guard, so a policy can tighten it to ask.
package remindcap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// channel is the host-owned notification target (same as notify): a reminder is a
// scheduled notification, never model-addressed.
const channel = "user"

// Pusher delivers the reminder at fire time — *ntfy.Publisher (phone) or the TUI
// console pusher satisfy it, exactly like notifycap.Pusher.
type Pusher interface {
	Push(ctx context.Context, title, message string) error
}

// Reminders is the reminder capability group. It owns the store (persistence) and
// its own pending timers (timing).
type Reminders struct {
	Guard   *gateway.Guard
	Store   *Store
	Push    Pusher
	Scanner *secret.Scanner
	// OnChange, if set, fires when the pending-reminder LIST changes (a reminder created,
	// fired, or cancelled) so a host can push the full list to clients — coarse live sync,
	// mirroring chat.Manager.OnChange. Optional; must not block.
	OnChange func()

	seq    atomic.Uint64
	mu     sync.Mutex
	timers map[string]*time.Timer // id -> pending one-shot timer
}

// changed fires the optional list-change hook (a reminder was created/fired/cancelled).
func (r *Reminders) changed() {
	if r.OnChange != nil {
		r.OnChange()
	}
}

// New builds the reminder capability. Call Restore once after construction to
// re-enroll reminders persisted from a previous run.
func New(guard *gateway.Guard, store *Store, push Pusher, scanner *secret.Scanner) *Reminders {
	return &Reminders{Guard: guard, Store: store, Push: push, Scanner: scanner, timers: map[string]*time.Timer{}}
}

// Restore re-enrolls every persisted reminder (call at startup). An overdue reminder
// (its time passed while the process was down) fires promptly — the user still gets
// it, just late.
func (r *Reminders) Restore() {
	for _, rem := range r.Store.List() {
		r.enroll(rem)
	}
}

// enroll schedules a one-shot timer that fires the reminder at its time. A far-future
// or overdue fireAt is fine — the delay is clamped to >= 0 and Go timers handle long
// durations.
func (r *Reminders) enroll(rem Reminder) {
	delay := max(time.Until(rem.FireAt), 0)
	r.mu.Lock()
	r.timers[rem.ID] = time.AfterFunc(delay, func() { r.fire(rem) })
	r.mu.Unlock()
}

// fire delivers the reminder: it re-scans the message (belt-and-suspenders — the vault
// may have changed since create), delivers it, and removes it from the store and the
// pending set. Delivery is skipped if the message now trips the scanner.
func (r *Reminders) fire(rem Reminder) {
	r.mu.Lock()
	delete(r.timers, rem.ID)
	r.mu.Unlock()
	r.Store.Remove(rem.ID)
	r.changed() // the pending list shrank
	if err := r.Scanner.ScanEgress(rem.Title, rem.Message); err != nil {
		return // a stored value now leaks — drop it rather than deliver
	}
	_ = r.Push.Push(context.Background(), rem.Title, rem.Message)
}

// cancel stops a pending reminder's timer and removes it from the store; it reports
// whether the reminder existed.
func (r *Reminders) cancel(id string) bool {
	r.mu.Lock()
	if t := r.timers[id]; t != nil {
		t.Stop()
		delete(r.timers, id)
	}
	r.mu.Unlock()
	existed := r.Store.Remove(id)
	if existed {
		r.changed()
	}
	return existed
}

// Cancel stops every pending timer (for a clean workspace/app shutdown). Reminders
// stay persisted, so they re-enroll on the next start.
func (r *Reminders) Cancel() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, t := range r.timers {
		t.Stop()
		delete(r.timers, id)
	}
}

// Pending reports how many reminders are scheduled (for tests).
func (r *Reminders) Pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.timers)
}

// Tools exposes remind (create), remind.list, and remind.cancel.
func (r *Reminders) Tools() []tool.Tool {
	return []tool.Tool{r.createTool(), r.listTool(), r.cancelTool()}
}

func (r *Reminders) createTool() tool.Tool {
	return tool.Tool{
		Spec: tool.Spec{
			Name: "remind",
			Description: "Schedule a reminder: at the given time, the user is notified with `message` (no model run, " +
				"the text is fixed now). `when` is either an absolute RFC3339 timestamp or \"in <duration>\" " +
				"(e.g. \"in 2h\", \"in 90m\"). For a wall-clock time like \"tomorrow 9am\", compute it with time.now " +
				"and pass the RFC3339 result. Returns {id, fireAt}.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"when":{"type":"string","description":"Absolute RFC3339 time, or \"in <duration>\" like \"in 2h\""},` +
				`"message":{"type":"string","description":"What to remind the user about"},` +
				`"title":{"type":"string","description":"Optional short title"}` +
				`},"required":["when","message"]}`),
		},
		Invoke: func(ctx context.Context, args string) (string, error) {
			var a struct {
				When    string `json:"when"`
				Message string `json:"message"`
				Title   string `json:"title"`
			}
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if strings.TrimSpace(a.Message) == "" {
				return "", errors.New("missing required field: message")
			}
			fireAt, err := r.parseWhen(a.When)
			if err != nil {
				return "", err
			}
			call := capability.Call{Family: "remind", Write: false, Target: channel}
			intent := "remind at " + fireAt.Format(time.RFC3339) + ": " + a.Message
			return gateway.Do(ctx, r.Guard, call, intent,
				gateway.ScanEgress(r.Scanner, func() []string { return []string{a.Title, a.Message} }),
				func() (string, error) {
					rem := Reminder{ID: r.newID(), FireAt: fireAt, Message: a.Message, Title: a.Title}
					if err := r.Store.Add(rem); err != nil {
						return "", err
					}
					r.enroll(rem)
					r.changed() // a new pending reminder appeared
					b, _ := json.Marshal(struct {
						ID     string `json:"id"`
						FireAt string `json:"fireAt"`
					}{rem.ID, fireAt.Format(time.RFC3339)})
					return string(b), nil
				})
		},
	}
}

func (r *Reminders) listTool() tool.Tool {
	return tool.Tool{
		Spec: tool.Spec{
			Name:        "remind.list",
			Description: "List the user's pending reminders. Returns a JSON array of {id, fireAt, message, title}.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		},
		Invoke: func(ctx context.Context, _ string) (string, error) {
			call := capability.Call{Family: "remind", Write: false, Target: channel}
			return gateway.Do(ctx, r.Guard, call, "list reminders", gateway.WithoutScan(), func() (string, error) {
				b, _ := json.Marshal(r.Store.List())
				return string(b), nil
			})
		},
	}
}

func (r *Reminders) cancelTool() tool.Tool {
	return tool.Tool{
		Spec: tool.Spec{
			Name:        "remind.cancel",
			Description: "Cancel a pending reminder by its id. Returns {id, cancelled}.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"id":{"type":"string","description":"The reminder id"}},"required":["id"]}`),
		},
		Invoke: func(ctx context.Context, args string) (string, error) {
			var a struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if a.ID == "" {
				return "", errors.New("missing required field: id")
			}
			call := capability.Call{Family: "remind", Write: false, Target: channel}
			return gateway.Do(ctx, r.Guard, call, "cancel reminder "+a.ID, gateway.WithoutScan(), func() (string, error) {
				existed := r.cancel(a.ID)
				b, _ := json.Marshal(struct {
					ID        string `json:"id"`
					Cancelled bool   `json:"cancelled"`
				}{a.ID, existed})
				return string(b), nil
			})
		},
	}
}

// parseWhen accepts an absolute RFC3339 time or "in <go-duration>" (relative to now).
func (r *Reminders) parseWhen(when string) (time.Time, error) {
	when = strings.TrimSpace(when)
	if when == "" {
		return time.Time{}, errors.New("missing required field: when")
	}
	if rest, ok := strings.CutPrefix(when, "in "); ok {
		d, err := time.ParseDuration(strings.TrimSpace(rest))
		if err != nil {
			return time.Time{}, fmt.Errorf("bad relative time %q: %w", when, err)
		}
		if d <= 0 {
			return time.Time{}, fmt.Errorf("reminder time must be in the future (got %q)", when)
		}
		return time.Now().Add(d), nil
	}
	t, err := time.Parse(time.RFC3339, when)
	if err != nil {
		return time.Time{}, fmt.Errorf("when must be RFC3339 or \"in <duration>\" (got %q): %w", when, err)
	}
	return t, nil
}

// newID mints a unique reminder id from the current time plus a per-instance counter.
func (r *Reminders) newID() string {
	return fmt.Sprintf("rem-%d-%d", time.Now().UnixNano(), r.seq.Add(1))
}
