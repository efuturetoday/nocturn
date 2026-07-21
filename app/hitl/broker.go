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
	Approval(id, intent string, options []string)
	// Resolved tells the connection an approval is concluded (answered anywhere, timed out, or no
	// longer needed) so it clears the prompt.
	Resolved(id string)
}

// Broker turns a gate Ask into an out-of-band decision. It implements gate.Approver.
type Broker struct {
	pusher Pusher
	log    *slog.Logger

	mu      sync.Mutex
	sinks   map[Sink]struct{}
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
		sinks:   map[Sink]struct{}{},
		pending: map[string]*pendingApproval{},
	}
}

// Attach registers a connection to receive approvals, re-presenting any already-open approvals so a
// device that connects mid-flight (or is woken by a push) can answer them. Detach removes it.
func (b *Broker) Attach(s Sink) {
	b.mu.Lock()
	b.sinks[s] = struct{}{}
	open := make(map[string]*pendingApproval, len(b.pending))
	maps.Copy(open, b.pending)
	b.mu.Unlock()
	for id, p := range open {
		s.Approval(id, p.intent, p.options)
	}
}

func (b *Broker) Detach(s Sink) {
	b.mu.Lock()
	delete(b.sinks, s)
	b.mu.Unlock()
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
	sinks := snapshot(b.sinks)
	b.mu.Unlock()
	defer b.conclude(id)

	if len(sinks) == 0 {
		// No device attached: reserve the out-of-band path. A real Pusher wakes a device, which
		// connects, sees the pending approval and resolves; the placeholder just logs and this Ask
		// times out to deny.
		if b.pusher != nil {
			if err := b.pusher.Push(ctx, intent); err != nil {
				b.log.Warn("hitl push failed", "err", err)
			}
		} else {
			b.log.Warn("hitl approval with no attached device — denying", "intent", intent)
		}
	} else {
		for s := range sinks {
			s.Approval(id, intent, labels)
		}
	}

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

// conclude clears the pending approval and tells every attached connection to remove its prompt,
// whatever the outcome (answered, timed out, cancelled).
func (b *Broker) conclude(id string) {
	b.mu.Lock()
	delete(b.pending, id)
	sinks := snapshot(b.sinks)
	b.mu.Unlock()
	for s := range sinks {
		s.Resolved(id)
	}
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

// snapshot copies the sink set so sinks are called without holding the lock.
func snapshot(m map[Sink]struct{}) map[Sink]struct{} {
	cp := make(map[Sink]struct{}, len(m))
	for s := range m {
		cp[s] = struct{}{}
	}
	return cp
}

func newID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic("hitl: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

var _ gate.Approver = (*Broker)(nil)
