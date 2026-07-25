// Package hitl routes a gate approval to a human out of band: it presents the request to every
// attached connection and takes the first answer (first-committed-wins), or wakes a device via a
// Pusher when none is attached. It implements gate.Approver, so a workspace's runtime asks it exactly
// like the terminal approver — the difference is the decision happens on a second device.
package hitl

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"maps"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/tools"
)

// approvalTimeout bounds how long an Ask waits before failing closed (deny).
const approvalTimeout = 2 * time.Minute

// ErrApprovalTimeout is returned by Ask when no human answered within approvalTimeout. The action is
// still denied (approved=false, fail-closed); the error only lets the caller tell a timeout apart
// from a deliberate human "no" (which returns a nil error so the gate surfaces it to the model).
var ErrApprovalTimeout = errors.New("hitl: approval timed out")

// Sink is a connection the broker can present an approval to and later tell to clear it. serve's
// connections implement it; the broker never imports serve.
type Sink interface {
	// Approval presents a pending approval: an intent to render and choice labels (index 0.. are
	// approvals, a client answers with the chosen index or -1 to deny). frame is the id of the tool
	// call this approval belongs to (opaque correlation — the connection forwards it so the UI can tie
	// the prompt to the exact call; 0 = not tool-scoped). chatID is the chat/agent run whose turn
	// raised it, for provenance ("" = not chat-scoped).
	Approval(ctx context.Context, id string, frame uint64, chatID, intent string, options []string)
	// Resolved tells the connection an approval is concluded (answered anywhere, timed out, or no
	// longer needed) so it clears the prompt.
	Resolved(ctx context.Context, id string)
}

// Broker turns a gate Ask into an out-of-band decision. It implements gate.Approver.
type Broker struct {
	pusher Pusher
	log    *slog.Logger

	mu      sync.Mutex
	sinks   map[Sink]bool               // attached connection → active (foreground)
	pending map[string]*pendingApproval // open approvals, by id
}

// pendingApproval is an approval awaiting a decision: what to present, and the channel a resolve is
// delivered on. Keeping intent/options lets the broker re-present it to a device that attaches while
// it is open (the reconnect / woken-by-push case).
type pendingApproval struct {
	intent  string
	options []string
	frame   uint64 // the tool call this approval is for (for re-present on attach)
	chatID  string // the chat/agent run whose turn raised it (for provenance on re-present)
	ch      chan int
}

// NewBroker builds a Broker. pusher may be nil (no out-of-band wake when no device is attached).
func NewBroker(pusher Pusher, log *slog.Logger) *Broker {
	return &Broker{
		pusher:  pusher,
		log:     log.With("component", "hitl"),
		sinks:   map[Sink]bool{},
		pending: map[string]*pendingApproval{},
	}
}

// Attach registers a connection, active (foreground) until it says otherwise, and re-presents any
// open approvals so a device that connects mid-flight (or is woken by a push) can answer them. ctx is
// the connection's, for the presenting sends.
func (b *Broker) Attach(ctx context.Context, s Sink) {
	b.mu.Lock()
	b.sinks[s] = true
	b.mu.Unlock()
	b.presentPending(ctx, s)
}

// Detach removes a connection.
func (b *Broker) Detach(s Sink) {
	b.mu.Lock()
	delete(b.sinks, s)
	b.mu.Unlock()
}

// SetActive marks a connection foreground/background. Approvals route only to active connections; a
// connection coming to the foreground gets the open approvals re-presented (the woken-by-push case).
func (b *Broker) SetActive(ctx context.Context, s Sink, active bool) {
	b.mu.Lock()
	if _, ok := b.sinks[s]; ok {
		b.sinks[s] = active
	}
	b.mu.Unlock()
	if active {
		b.presentPending(ctx, s)
	}
}

// presentPending shows every open approval to one connection.
func (b *Broker) presentPending(ctx context.Context, s Sink) {
	b.mu.Lock()
	open := make(map[string]*pendingApproval, len(b.pending))
	maps.Copy(open, b.pending)
	b.mu.Unlock()
	for id, p := range open {
		s.Approval(ctx, id, p.frame, p.chatID, p.intent, p.options)
	}
}

// Resolve delivers a connection's decision for approval id (a choice index, or -1 to deny). First
// answer wins; later ones are dropped.
func (b *Broker) Resolve(id string, choice int) {
	b.mu.Lock()
	p := b.pending[id]
	b.mu.Unlock()
	if p != nil {
		select {
		case p.ch <- choice:
		default:
		}
	}
}

// Ask implements gate.Approver: present the action to every attached connection (or wake a device
// via the Pusher when none is attached) and return the first decision. Every non-approval is
// fail-closed (approved=false), but the error distinguishes them: a deliberate human "no" returns a
// nil error (the gate surfaces it to the model as ErrDenied so it can adapt), a timeout returns
// ErrApprovalTimeout, and a cancelled turn returns ctx.Err() — so the caller can tell "the human
// declined" from "nobody answered" from "the turn was torn down". gate.Check pauses the turn clock
// around this call.
func (b *Broker) Ask(ctx context.Context, a gate.Action, suggest []gate.Grant) (bool, gate.Grant, gate.Recall, error) {
	labels, resolve := decision(a, suggest)
	id := newID()
	intent := intentOf(a)
	frame := agentkit.FrameFrom(ctx) // the tool call asking — for the UI to tie the prompt to it
	chatID := tools.ChatID(ctx)      // the chat/agent run whose turn raised it — for provenance
	ch := make(chan int, 1)

	b.mu.Lock()
	b.pending[id] = &pendingApproval{intent: intent, options: labels, frame: frame, chatID: chatID, ch: ch}
	b.mu.Unlock()

	active := b.activeSinks()
	if len(active) == 0 {
		// No active device: reserve the out-of-band path. A real Pusher wakes a device, which comes
		// to the foreground, gets the pending approval re-presented and resolves; the placeholder
		// just logs and this Ask times out to deny.
		if b.pusher != nil {
			if err := b.pusher.Push(ctx, intent); err != nil {
				b.log.Warn("hitl push failed", "err", err)
			}
		} else {
			b.log.Warn("hitl approval with no active device — denying", "intent", intent)
		}
	} else {
		for _, s := range active {
			s.Approval(ctx, id, frame, chatID, intent, labels)
		}
	}

	defer b.conclude(ctx, id)
	select {
	case choice := <-ch:
		return resolve(choice)
	case <-time.After(approvalTimeout):
		b.log.Warn("hitl approval timed out — denying", "intent", intent)
		return false, gate.Grant{}, gate.RecallNever, ErrApprovalTimeout
	case <-ctx.Done():
		return false, gate.Grant{}, gate.RecallNever, ctx.Err()
	}
}

// conclude clears the pending approval and tells every attached connection (active or not) to remove
// its prompt, whatever the outcome (answered, timed out, cancelled). It runs on WithoutCancel so a
// cancelled turn still clears the prompts it raised.
func (b *Broker) conclude(ctx context.Context, id string) {
	ctx = context.WithoutCancel(ctx)
	b.mu.Lock()
	delete(b.pending, id)
	all := make([]Sink, 0, len(b.sinks))
	for s := range b.sinks {
		all = append(all, s)
	}
	b.mu.Unlock()
	for _, s := range all {
		s.Resolved(ctx, id)
	}
}

// AnyActive reports whether any attached connection is currently in the foreground. It is the same
// presence that routes approvals, exposed so proactive notifications route the same either/or way: a
// message goes over the live connection when a device is watching, and out of band (a push) only when
// none is.
func (b *Broker) AnyActive() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, active := range b.sinks {
		if active {
			return true
		}
	}
	return false
}

// activeSinks returns the attached connections currently in the foreground.
func (b *Broker) activeSinks() []Sink {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Sink
	for s, active := range b.sinks {
		if active {
			out = append(out, s)
		}
	}
	return out
}

// decision builds the human choice labels for an action and the mapper from a chosen index back to
// (approved, grant, recall). Index 0 = allow once (RecallNever — nothing remembered, asks again next
// time), 1 = allow this session, 2 = allow always, 3.. = a suggested widening (always); anything else
// (e.g. -1) denies.
func decision(a gate.Action, suggest []gate.Grant) (labels []string, resolve func(choice int) (bool, gate.Grant, gate.Recall, error)) {
	exact := gate.Grant{Kind: a.Kind, Target: a.Target}
	grants := []gate.Grant{exact, exact, exact}
	recalls := []gate.Recall{gate.RecallNever, gate.RecallSession, gate.RecallAlways}
	labels = []string{"Once", "Session", "Always"}
	for _, s := range suggest {
		labels = append(labels, "Always: "+s.Target)
		grants = append(grants, s)
		recalls = append(recalls, gate.RecallAlways)
	}
	resolve = func(choice int) (bool, gate.Grant, gate.Recall, error) {
		if choice < 0 || choice >= len(grants) {
			return false, gate.Grant{}, gate.RecallNever, nil // deny
		}
		return true, grants[choice], recalls[choice], nil
	}
	return labels, resolve
}

func intentOf(a gate.Action) string {
	if a.Target != "" {
		return a.Kind + " → " + a.Target
	}
	return a.Kind
}

// newID returns a random 12-hex-char prompt id. It panics if crypto/rand fails,
// which on a healthy host never happens and signals the OS entropy source is broken.
func newID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic("hitl: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

var _ gate.Approver = (*Broker)(nil)
