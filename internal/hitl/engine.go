// Package hitl is the human-in-the-loop approval engine: it turns the broker's
// "Ask" verdict into a real, out-of-band decision. A sensitive call is queued
// and suspended; the human approves or denies on a separate device (e.g. a
// phone push); only an explicit approval lets the effect proceed. A timeout or
// cancellation denies — fail closed.
//
// This layer is the mechanism only: the Engine, the signed single-use token,
// and the Notifier seam. The real push transport (ntfy) is a thin Notifier
// implementation added later; wiring the Engine behind the broker's Ask happens
// with the task loop.
package hitl

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/internal/deadline"
)

// Outcome is the human's decision. The zero value is Denied — anything short of
// an explicit approval fails closed. ApprovedSession approves this call and asks
// the caller to remember the grant for the rest of the session.
type Outcome int

const (
	Denied Outcome = iota
	Approved
	ApprovedSession
	// ApprovedAlways approves this call and asks the caller to remember the grant
	// persistently (across restarts), scoped to the current context/workspace.
	ApprovedAlways
)

func (o Outcome) String() string {
	switch o {
	case Approved:
		return "Approved"
	case ApprovedSession:
		return "ApprovedSession"
	case ApprovedAlways:
		return "ApprovedAlways"
	default:
		return "Denied"
	}
}

// Choice is an approval option offered to the human: a label and the outcome it
// produces (e.g. "Allow once" -> Approved, "Allow this session" ->
// ApprovedSession, "Deny" -> Denied).
type Choice struct {
	Label   string
	Outcome Outcome
}

// Option is a Choice made concrete with a signed, single-use token to send back
// when the human picks it.
type Option struct {
	Label   string
	Outcome Outcome
	Token   string
}

// Notifier delivers an approval request out of band with a set of options, each
// carrying a ready-to-use token. A real implementation renders the labels (a
// phone push with action buttons, a TUI select) and, when the human picks one,
// sends its token back to Resolve.
type Notifier interface {
	Notify(intent string, options []Option) error
}

type pending struct {
	nonce    string
	resolved chan Outcome
}

// Engine issues approval requests and resolves the decisions that come back.
type Engine struct {
	key      []byte
	notifier Notifier
	route    func(ctx context.Context) Notifier
	now      func() time.Time

	mu      sync.Mutex
	pending map[string]*pending
}

// EngineOption configures an Engine.
type EngineOption func(*Engine)

// WithRouter picks the Notifier for each request from its context — e.g. an
// interactive channel for an attended run and an out-of-band channel (phone) for an
// unattended one. Returning nil uses the engine's default notifier. This is where
// "which human, which device" lives: the engine owns the channel — and, being the
// one component that holds every channel AND the wait loop, it is also where a future
// escalation (interactive times out → try out-of-band) would live. A single engine
// means one token space, so any channel can resolve the same pending request.
func WithRouter(route func(ctx context.Context) Notifier) EngineOption {
	return func(e *Engine) { e.route = route }
}

// NewEngine returns an engine that signs tokens with key and notifies via n (the
// default channel; WithRouter can override it per request).
func NewEngine(key []byte, n Notifier, opts ...EngineOption) *Engine {
	e := &Engine{key: key, notifier: n, now: time.Now, pending: make(map[string]*pending)}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Request escalates an intent to a human out of band, offering the given
// choices, and blocks until a decision arrives, the ttl expires, or ctx is
// cancelled. Anything but an explicit approval returns Denied (fail closed).
func (e *Engine) Request(ctx context.Context, intent string, choices []Choice, ttl time.Duration) (Outcome, error) {
	id, nonce := randID(), randID()
	expires := e.now().Add(ttl)

	p := &pending{nonce: nonce, resolved: make(chan Outcome, 1)}
	e.mu.Lock()
	e.pending[id] = p
	e.mu.Unlock()

	options := make([]Option, len(choices))
	for i, c := range choices {
		options[i] = Option{
			Label:   c.Label,
			Outcome: c.Outcome,
			Token:   sign(e.key, token{id: id, nonce: nonce, expires: expires.Unix(), outcome: c.Outcome}),
		}
	}
	// The caller is now parked on a human. Pause any execution budget on ctx so
	// this wait consumes the HITL TTL, not the guest/tool execution deadline; it
	// resumes when the human answers (or on timeout/cancel). Placed before Notify
	// so slow notify I/O (e.g. the ntfy push) is off-budget too; the defer also
	// balances the notify-error early return below.
	if p := deadline.PauserFrom(ctx); p != nil {
		p.Pause()
		defer p.Resume()
	}

	// Pick the channel for this request: the router (if set) decides from ctx —
	// attended → interactive, unattended → out-of-band — falling back to the default.
	notifier := e.notifier
	if e.route != nil {
		if r := e.route(ctx); r != nil {
			notifier = r
		}
	}
	if err := notifier.Notify(intent, options); err != nil {
		e.discard(id)
		return Denied, err
	}

	ctx, cancel := context.WithTimeout(ctx, ttl)
	defer cancel()
	select {
	case out := <-p.resolved:
		return out, nil
	case <-ctx.Done():
		e.discard(id)
		return Denied, nil // timeout / cancellation denies, fail closed
	}
}

// Resolve applies a decision carried by a token from the out-of-band channel.
// It verifies the token's signature and expiry, matches it to a live pending
// request (by id and nonce), and consumes it single-use.
func (e *Engine) Resolve(tokenStr string) error {
	t, err := verifyToken(e.key, tokenStr, e.now().Unix())
	if err != nil {
		return err
	}
	e.mu.Lock()
	p, ok := e.pending[t.id]
	if !ok || p.nonce != t.nonce {
		e.mu.Unlock()
		return fmt.Errorf("hitl: no matching pending request (already resolved, expired, or unknown)")
	}
	delete(e.pending, t.id) // single-use: consume before delivering
	e.mu.Unlock()

	p.resolved <- t.outcome
	return nil
}

func (e *Engine) discard(id string) {
	e.mu.Lock()
	delete(e.pending, id)
	e.mu.Unlock()
}

func randID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
