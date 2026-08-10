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
	"strconv"
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

// DenyOption is the reserved option id that explicitly refuses. It is never minted as a real option,
// so it cannot collide with one; and because any unrecognised id refuses too, a client that cannot
// say the word still falls closed.
const DenyOption = "deny"

// Approval is one pending approval as presented to a Sink: the action, where it came from, and the
// answers on offer. It is structure, not prose — the only text in it (the Action's kind and target, a
// widening's target) comes from the gate and from the asking tool, never from the model. The device
// does the wording.
type Approval struct {
	ID      string      // this approval's id; a Sink answers with it
	Frame   uint64      // the tool call this approval belongs to (opaque correlation; 0 = not tool-scoped)
	ChatID  string      // the chat/agent run whose turn raised it, for provenance ("" = not chat-scoped)
	Action  gate.Action // what is being asked, verbatim
	Options []Option    // the answers on offer, in presentation order; never empty
}

// Option is one answer on offer. ID is what a client sends back to choose it — minted here, so a
// client can only pick a grant this broker offered, never one it named itself. Grant is exactly what
// choosing it remembers and Recall for how long (RecallNever remembers nothing; the grant is still
// stated so every option has one shape). Widens says the Grant is BROADER than the Action — a tool's
// suggested widening — so no layer downstream has to re-derive that by comparing strings.
type Option struct {
	ID     string
	Recall gate.Recall
	Grant  gate.Grant
	Widens bool
}

// Sink is a connection the broker can present an approval to and later tell to clear it. serve's
// connections implement it; the broker never imports serve.
type Sink interface {
	// Approval presents a pending approval. The Options slice is shared with the broker and with
	// every other Sink — read it, never mutate it.
	Approval(ctx context.Context, a Approval)
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

// pendingApproval is an approval awaiting a decision: exactly what any Sink is (re)presented, and
// the channel the chosen option id is delivered on. Keeping the whole presentation lets the broker
// re-present it to a device that attaches while it is open (the reconnect / woken-by-push case), and
// because each Option carries its own grant and recall, it is also what resolves the answer.
type pendingApproval struct {
	present Approval
	ch      chan string
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

// The connection-bookkeeping methods — Attach, Detach, SetActive, Resolve, AnyActive — all tolerate a
// NIL Broker and do nothing.
//
// That is what makes "absence rather than a check" true rather than merely intended. A device whose
// class may not approve is handed no broker at all (internal/serve, at accept), so its connection
// holds a nil *Broker for its whole life and calls these on it. Leaving them to explode put the same
// three-line guard at every call site, and the day one site forgot — presence.set — a single message
// from the least-trusted class panicked the read loop and killed the connection. One check on the
// receiver cannot be forgotten by a caller that does not exist yet.
//
// Ask is deliberately NOT in that list. What an absent approver ANSWERS is a permission decision, and
// gate already owns it ("nil approver = unattended, so any Ask is denied"). A second nil-tolerant
// answer here would be a second place deciding the same thing, and the two could disagree.

// Attach registers a connection, active (foreground) until it says otherwise, and re-presents any
// open approvals so a device that connects mid-flight (or is woken by a push) can answer them. ctx is
// the connection's, for the presenting sends.
func (b *Broker) Attach(ctx context.Context, s Sink) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.sinks[s] = true
	b.mu.Unlock()
	b.presentPending(ctx, s)
}

// Detach removes a connection.
func (b *Broker) Detach(s Sink) {
	if b == nil {
		return
	}
	b.mu.Lock()
	delete(b.sinks, s)
	b.mu.Unlock()
}

// SetActive marks a connection foreground/background. Approvals route only to active connections; a
// connection coming to the foreground gets the open approvals re-presented (the woken-by-push case).
func (b *Broker) SetActive(ctx context.Context, s Sink, active bool) {
	if b == nil {
		return
	}
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
	for _, p := range open {
		s.Approval(ctx, p.present)
	}
}

// Resolve delivers a connection's decision for approval id: the id of the chosen option, or
// DenyOption. First answer wins; later ones are dropped. An id this approval never offered refuses,
// so a stale or forged answer cannot approve.
func (b *Broker) Resolve(id, option string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	p := b.pending[id]
	b.mu.Unlock()
	if p != nil {
		select {
		case p.ch <- option:
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
	opts := options(a, suggest)
	present := Approval{
		ID:      newID(),
		Frame:   agentkit.FrameFrom(ctx), // the tool call asking — for the UI to tie the prompt to it
		ChatID:  tools.ChatID(ctx),       // the chat/agent run whose turn raised it — for provenance
		Action:  a,
		Options: opts,
	}
	ch := make(chan string, 1)

	b.mu.Lock()
	b.pending[present.ID] = &pendingApproval{present: present, ch: ch}
	b.mu.Unlock()

	active := b.activeSinks()
	if len(active) == 0 {
		// No active device: reserve the out-of-band path. A real Pusher wakes a device, which comes
		// to the foreground, gets the pending approval re-presented and resolves; the placeholder
		// just logs and this Ask times out to deny.
		if b.pusher != nil {
			if err := b.pusher.Push(ctx, summary(a)); err != nil {
				b.log.Warn("hitl push failed", "err", err)
			}
		} else {
			b.log.Warn("hitl approval with no active device — denying", "kind", a.Kind, "target", a.Target)
		}
	} else {
		for _, s := range active {
			s.Approval(ctx, present)
		}
	}

	defer b.conclude(ctx, present.ID)
	select {
	case option := <-ch:
		return pick(opts, option)
	case <-time.After(approvalTimeout):
		b.log.Warn("hitl approval timed out — denying", "kind", a.Kind, "target", a.Target)
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
	if b == nil {
		return false
	}
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

// options builds the answers on offer for an action: allow once (RecallNever — nothing remembered,
// asks again next time), for this session, always — each on the action's EXACT target — then the
// tool's suggested widenings, each always-remembered. The order is the presentation order. The ids
// are opaque to a client, which chooses by echoing one back; a client keying off them instead of off
// Recall and Widens is a client bug, because this mapping is the authoritative one either way.
func options(a gate.Action, suggest []gate.Grant) []Option {
	exact := gate.Grant{Kind: a.Kind, Target: a.Target}
	opts := []Option{
		{ID: "once", Recall: gate.RecallNever, Grant: exact},
		{ID: "session", Recall: gate.RecallSession, Grant: exact},
		{ID: "always", Recall: gate.RecallAlways, Grant: exact},
	}
	for i, s := range suggest {
		opts = append(opts, Option{
			ID:     "widen" + strconv.Itoa(i),
			Recall: gate.RecallAlways,
			Grant:  s,
			Widens: true,
		})
	}
	return opts
}

// pick maps a chosen option id back to a decision. An id that was never offered — DenyOption, an
// unknown one, or the empty string a truncated message leaves behind — is a refusal with a nil
// error: the same deliberate "no" the gate surfaces to the model. There is no value that approves by
// default.
func pick(opts []Option, id string) (bool, gate.Grant, gate.Recall, error) {
	for _, o := range opts {
		if o.ID == id {
			return true, o.Grant, o.Recall, nil
		}
	}
	return false, gate.Grant{}, gate.RecallNever, nil
}

// summary renders the one line a push notification carries: a lock-screen wake, not the ask. It is
// the only prose this package produces, and it exists because iOS renders that string and we cannot
// hand it structure. The ask itself is rendered by the device from the Approval.
func summary(a gate.Action) string {
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
