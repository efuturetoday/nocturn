// Package hitl routes a gate approval to a human out of band: it presents the request to every
// attached connection and takes the first answer (first-committed-wins), or wakes a device via a
// Pusher when none is attached. It implements gate.Approver, so a workspace's runtime asks it exactly
// like the terminal approver — the difference is the decision happens on a second device.
package hitl

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"maps"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/agentkit/gate"
)

// approvalTimeout bounds how long an Ask waits before failing closed (deny).
const approvalTimeout = 2 * time.Minute

// Sink is a connection the broker can present an approval to and later tell to clear it. serve's
// connections implement it; the broker never imports serve.
type Sink interface {
	// Approval presents a pending approval: an intent to render and choice labels (index 0.. are
	// approvals, a client answers with the chosen index or -1 to deny).
	Approval(ctx context.Context, id, intent string, options []string)
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
	ch      chan int
}

// NewBroker builds a Broker. pusher may be nil (no out-of-band wake when no device is attached).
func NewBroker(pusher Pusher, log *slog.Logger) *Broker {
	return &Broker{
		pusher:  pusher,
		log:     log,
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
		s.Approval(ctx, id, p.intent, p.options)
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
// via the Pusher when none is attached) and return the first decision, or deny on timeout or when
// the turn is cancelled (no longer needed). gate.Check pauses the turn clock around this call.
func (b *Broker) Ask(ctx context.Context, a gate.Action, suggest []gate.Grant) (bool, gate.Grant, gate.Recall, error) {
	labels, resolve := decision(a, suggest)
	id := newID()
	intent := intentOf(a)
	ch := make(chan int, 1)

	b.mu.Lock()
	b.pending[id] = &pendingApproval{intent: intent, options: labels, ch: ch}
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
			s.Approval(ctx, id, intent, labels)
		}
	}

	defer b.conclude(ctx, id)
	select {
	case choice := <-ch:
		return resolve(choice)
	case <-time.After(approvalTimeout):
		b.log.Warn("hitl approval timed out — denying", "intent", intent)
		return false, gate.Grant{}, gate.RecallNever, nil
	case <-ctx.Done():
		return false, gate.Grant{}, gate.RecallNever, nil
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
// (approved, grant, recall). Index 0 = allow this session, 1 = allow always, 2.. = a suggested
// widening (always); anything else denies.
func decision(a gate.Action, suggest []gate.Grant) (labels []string, resolve func(choice int) (bool, gate.Grant, gate.Recall, error)) {
	exact := gate.Grant{Kind: a.Kind, Target: a.Target}
	grants := []gate.Grant{exact, exact}
	recalls := []gate.Recall{gate.RecallSession, gate.RecallAlways}
	labels = []string{"allow once (session)", "allow always"}
	for _, s := range suggest {
		labels = append(labels, "always allow "+s.Target)
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

func newID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic("hitl: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

var _ gate.Approver = (*Broker)(nil)
