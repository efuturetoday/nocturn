package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// RemindKind is the gate Kind the reminder tools check; like NotifyKind the Target is the host-owned
// "user" channel. Reminders run silently under the base policy (a reminder notifies the user, it does
// nothing else), but pass the gate so a stricter policy can tighten them.
const RemindKind = "remind"

// Reminder is one scheduled notification, persisted so it survives a restart. ChatID is the chat it
// was set in, captured at creation because a fire has no ctx to read it from — it is what lets a tap
// on the delivered notification land back in the conversation that asked for it. Empty when the
// reminder was set outside any chat.
type Reminder struct {
	ID      string    `json:"id"`
	FireAt  time.Time `json:"fireAt"`
	Message string    `json:"message"`
	Title   string    `json:"title,omitempty"`
	ChatID  string    `json:"chatId,omitempty"`
}

// Reminders is the persistent reminder tool group: it owns a JSON-file store and one time.AfterFunc
// per pending reminder. A fire delivers a plain notification through the Notifier — it never runs the
// model. The store is a control-plane file OUTSIDE the model's file mount, load-bearing like grants.
type Reminders struct {
	path     string
	notifier Notifier
	scanner  *secret.Scanner
	log      *slog.Logger

	mu       sync.Mutex
	items    map[string]Reminder
	timers   map[string]*time.Timer
	onChange func()
	seq      atomic.Uint64
}

// NewReminders opens (tolerantly — a missing or malformed file is an empty store) a reminder store at
// path, delivering fires through notifier. Call Restore once after construction to enroll the
// persisted reminders. An empty path is in-memory only (tests).
func NewReminders(path string, notifier Notifier, scanner *secret.Scanner) *Reminders {
	r := &Reminders{
		path:     path,
		notifier: notifier,
		scanner:  scanner,
		log:      slog.New(slog.DiscardHandler),
		items:    map[string]Reminder{},
		timers:   map[string]*time.Timer{},
	}
	r.load()
	return r
}

// SetLogger attaches a logger so a fire dropped by the re-scan is not silent. nil is ignored.
func (r *Reminders) SetLogger(l *slog.Logger) {
	if l != nil {
		r.log = l
	}
}

// OnChange registers the callback run after the pending set changes (a create, a cancel, a fire), so
// a listing UI can refresh without polling. Set once, at wiring time, before serving. The callback
// runs OUTSIDE r.mu — it may call back into List.
func (r *Reminders) OnChange(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onChange = fn
}

// changed fires the change callback. Callers must NOT hold r.mu.
func (r *Reminders) changed() {
	r.mu.Lock()
	fn := r.onChange
	r.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// Tools exposes remind / remind_list / remind_cancel.
func (r *Reminders) Tools() ([]agentkit.Tool, error) {
	create, err := agentkit.NewTool("remind",
		`Schedule a reminder that notifies the user at a future time. "when" is either "in <duration>" (e.g. "in 30m", "in 2h") or an RFC3339 timestamp. Returns {"id", "fireAt"}.`,
		r.create,
		agentkit.WithSchema(agentkit.Object(
			agentkit.Prop("when", agentkit.String(`When to fire: "in <duration>" or an RFC3339 timestamp`)),
			agentkit.Prop("message", agentkit.String("The reminder message")),
			agentkit.Prop("title", agentkit.String("Optional title")),
		).Require("when", "message")),
	)
	if err != nil {
		return nil, err
	}
	list, err := agentkit.NewTool("remind_list",
		"List the pending reminders, soonest first. Returns a JSON array of {id, fireAt, message, title}.",
		r.list,
		agentkit.WithSchema(agentkit.Object()),
	)
	if err != nil {
		return nil, err
	}
	cancel, err := agentkit.NewTool("remind_cancel",
		`Cancel a pending reminder by id. Returns {"id", "cancelled"}.`,
		r.cancelTool,
		agentkit.WithSchema(agentkit.Object(
			agentkit.Prop("id", agentkit.String("The reminder id to cancel")),
		).Require("id")),
	)
	if err != nil {
		return nil, err
	}
	return []agentkit.Tool{create, list, cancel}, nil
}

func (r *Reminders) create(ctx context.Context, args string) (string, error) {
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
	fireAt, err := parseWhen(a.When)
	if err != nil {
		return "", err
	}
	if err := gate.Check(ctx, gate.Action{Kind: RemindKind, Target: notifyChannel}, nil); err != nil {
		return "", err
	}
	// Egress: the reminder text is scheduled to leave the box to the user's device — block a smuggled
	// secret at creation time (it is re-scanned at fire time too).
	if r.scanner != nil {
		if err := r.scanner.ScanEgress(a.Title, a.Message); err != nil {
			return "", fmt.Errorf("egress blocked: %w", err)
		}
	}
	rem := Reminder{
		ID:      fmt.Sprintf("rem-%d-%d", time.Now().UnixNano(), r.seq.Add(1)),
		FireAt:  fireAt,
		Message: a.Message,
		Title:   a.Title,
		ChatID:  ChatID(ctx), // captured now: the fire runs on a timer, with no ctx to read
	}
	r.mu.Lock()
	r.items[rem.ID] = rem
	r.save()
	r.enroll(rem)
	r.mu.Unlock()
	r.changed()
	return jsonResult(struct {
		ID     string `json:"id"`
		FireAt string `json:"fireAt"`
	}{rem.ID, rem.FireAt.Format(time.RFC3339)})
}

func (r *Reminders) list(context.Context, string) (string, error) {
	return jsonResult(r.List())
}

// List returns the pending reminders, soonest first. A fired reminder is gone (fire removes it), so
// this is the pending set, never a history. It backs both the model's remind_list tool and the
// companion app's listing.
func (r *Reminders) List() []Reminder {
	r.mu.Lock()
	out := make([]Reminder, 0, len(r.items))
	for _, rem := range r.items {
		out = append(out, rem)
	}
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].FireAt.Before(out[j].FireAt) })
	return out
}

// CancelByID drops a pending reminder and stops its timer, reporting whether it existed. It is the
// host-side cancel (the companion app); the model cancels through the remind_cancel tool.
func (r *Reminders) CancelByID(id string) bool {
	r.mu.Lock()
	_, ok := r.items[id]
	if ok {
		r.stopAndRemove(id)
		r.save()
	}
	r.mu.Unlock()
	if ok {
		r.changed()
	}
	return ok
}

func (r *Reminders) cancelTool(_ context.Context, args string) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.ID == "" {
		return "", errors.New("missing required field: id")
	}
	ok := r.CancelByID(a.ID)
	return jsonResult(struct {
		ID        string `json:"id"`
		Cancelled bool   `json:"cancelled"`
	}{a.ID, ok})
}

// Restore enrolls every persisted reminder; overdue ones fire promptly (the delay is clamped to ≥0).
// Call it once after NewReminders.
func (r *Reminders) Restore() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rem := range r.items {
		r.enroll(rem)
	}
}

// Cancel stops every pending timer (leaving the reminders persisted, to be re-enrolled next start).
func (r *Reminders) Cancel() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, t := range r.timers {
		t.Stop()
		delete(r.timers, id)
	}
}

// enroll arms a timer for one reminder. Callers hold r.mu.
func (r *Reminders) enroll(rem Reminder) {
	if t, ok := r.timers[rem.ID]; ok {
		t.Stop()
	}
	delay := time.Until(rem.FireAt)
	if delay < 0 {
		delay = 0
	}
	id := rem.ID
	r.timers[id] = time.AfterFunc(delay, func() { r.fire(id) })
}

// stopAndRemove stops a reminder's timer and drops it from the store. Callers hold r.mu.
func (r *Reminders) stopAndRemove(id string) {
	if t, ok := r.timers[id]; ok {
		t.Stop()
		delete(r.timers, id)
	}
	delete(r.items, id)
}

// fire delivers one reminder as a notification (no model run) and removes it.
func (r *Reminders) fire(id string) {
	r.mu.Lock()
	rem, ok := r.items[id]
	if ok {
		r.stopAndRemove(id)
		r.save()
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	// The pending set shrank whether or not the delivery below succeeds, so refresh any listing now.
	r.changed()
	// Re-scan at fire time: a secret may have been stored between creation and now (or the ruleset
	// grew) — drop silently rather than deliver a leak.
	if r.scanner != nil {
		if err := r.scanner.ScanEgress(rem.Title, rem.Message); err != nil {
			r.log.Warn("reminder dropped — egress scan flagged its text", "id", id)
			return
		}
	}
	// The reminder is already out of the store, so a failed delivery loses it for good. fire runs on a
	// timer and has no caller to return to — log it, or the one thing this feature exists for fails
	// with no trace at all.
	if err := r.notifier.Notify(context.Background(), Notification{
		Kind: RemindKind, ChatID: rem.ChatID, Title: rem.Title, Message: rem.Message,
	}); err != nil {
		r.log.Warn("reminder fired but delivery failed — it is no longer pending", "id", id, "err", err)
	}
}

// load reads the persisted reminders (tolerant: missing/malformed → empty). Called once at construction.
func (r *Reminders) load() {
	if r.path == "" {
		return
	}
	data, err := os.ReadFile(r.path)
	if err != nil {
		return
	}
	var list []Reminder
	if err := json.Unmarshal(data, &list); err != nil {
		return
	}
	for _, rem := range list {
		r.items[rem.ID] = rem
	}
}

// save persists the reminder set atomically (write then rename), 0600. Callers hold r.mu.
func (r *Reminders) save() {
	if r.path == "" {
		return
	}
	list := make([]Reminder, 0, len(r.items))
	for _, rem := range r.items {
		list = append(list, rem)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].FireAt.Before(list[j].FireAt) })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, r.path)
}

// parseWhen accepts "in <go-duration>" (must be > 0) or an RFC3339 timestamp.
func parseWhen(when string) (time.Time, error) {
	when = strings.TrimSpace(when)
	if rest, ok := strings.CutPrefix(when, "in "); ok {
		d, err := time.ParseDuration(strings.TrimSpace(rest))
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid duration %q: %w", rest, err)
		}
		if d <= 0 {
			return time.Time{}, fmt.Errorf("duration must be positive, got %q", rest)
		}
		return time.Now().Add(d), nil
	}
	t, err := time.Parse(time.RFC3339, when)
	if err != nil {
		return time.Time{}, fmt.Errorf(`invalid "when" %q: use "in <duration>" or an RFC3339 timestamp`, when)
	}
	return t, nil
}
