package tui

import (
	"context"
	"errors"
	"strconv"
	"sync"

	"github.com/efuturetoday/nocturn/agentkit/gate"
)

// ErrClosed is returned to a caller still waiting on an answer when the UI shuts down. It is a
// failure to ask, not a refusal — the gate turns either into a denial, but the distinction belongs
// in the log.
var ErrClosed = errors.New("tui: approval surface closed")

// Option is one answer on offer. The set is minted here from the action and the tool's suggested
// widenings, never from anything the model wrote; a client can only choose among them.
type Option struct {
	Label  string // what the modal shows
	Grant  gate.Grant
	Recall gate.Recall
	Widens bool // grants more than the action asked for — the modal marks these
}

// Ask is one pending approval handed to the UI. The UI answers it with Resolve or Deny from the
// event loop; both are safe to call more than once, and after the asking turn has already given up.
type Ask struct {
	Action  gate.Action
	Options []Option

	reply chan int // buffered 1, so an answer never blocks the event loop
}

// Resolve answers with the i-th option. An index the ask never offered is a refusal — there is no
// value that approves by default.
func (a *Ask) Resolve(i int) {
	select {
	case a.reply <- i:
	default: // already answered; the first answer stands
	}
}

// Deny refuses the action.
func (a *Ask) Deny() { a.Resolve(-1) }

// Approver presents approvals in the terminal UI. It is the local sibling of internal/hitl's
// broker: same job, different surface, and deliberately not the same code — the broker carries
// multi-device presence, re-presentation on reconnect and push wakeups, none of which exist when
// the person is at the keyboard. What it does reuse is the broker's SEMANTICS: the same option set
// in the same order, and the same rule that an answer nobody offered is a refusal.
//
// Unlike the broker there is no timeout. Two minutes exists out of band because nobody may be
// looking; here somebody is, gate.Check pauses the turn's clock around the call, and Ctrl+C is the
// honest way out. A deadline would only refuse what the user was still reading.
type Approver struct {
	asks    chan *Ask
	cleared chan *Ask
	done    chan struct{}
	once    sync.Once
}

var _ gate.Approver = (*Approver)(nil)

// NewApprover returns an approver whose channels the UI watches.
func NewApprover() *Approver {
	return &Approver{
		asks:    make(chan *Ask),
		cleared: make(chan *Ask, 8),
		done:    make(chan struct{}),
	}
}

// Asks delivers each pending approval to the UI. It is unbuffered: an ask that nobody is there to
// present is an ask that must keep waiting, not one that vanishes into a queue.
func (p *Approver) Asks() <-chan *Ask { return p.asks }

// Cleared reports asks that are no longer live — the turn was cancelled, or the UI is shutting down
// — so a modal presenting one closes instead of waiting for an answer that can no longer matter.
func (p *Approver) Cleared() <-chan *Ask { return p.cleared }

// Close releases every caller still waiting. Idempotent, and safe to call from the UI's shutdown
// path while turns are still in flight.
func (p *Approver) Close() { p.once.Do(func() { close(p.done) }) }

// Ask presents the action and blocks until the person answers, the turn is cancelled, or the UI
// closes. It implements gate.Approver.
func (p *Approver) Ask(ctx context.Context, a gate.Action, ceiling gate.Recall, suggest []gate.Grant) (bool, gate.Grant, gate.Recall, error) {
	ask := &Ask{Action: a, Options: options(a, ceiling, suggest), reply: make(chan int, 1)}

	select {
	case p.asks <- ask:
	case <-ctx.Done():
		return deny(ctx.Err())
	case <-p.done:
		return deny(ErrClosed)
	}
	defer p.clear(ask)

	select {
	case i := <-ask.reply:
		if i < 0 || i >= len(ask.Options) {
			return deny(nil) // a deliberate no, not a failure
		}
		o := ask.Options[i]
		return true, o.Grant, o.Recall, nil
	case <-ctx.Done():
		return deny(ctx.Err())
	case <-p.done:
		return deny(ErrClosed)
	}
}

// clear tells the UI the ask is over. Best-effort: a full channel means the UI is gone or wedged,
// and a stuck modal is not worth blocking the turn that is trying to finish.
func (p *Approver) clear(ask *Ask) {
	select {
	case p.cleared <- ask:
	default:
	}
}

func deny(err error) (bool, gate.Grant, gate.Recall, error) {
	return false, gate.Grant{}, gate.RecallNever, err
}

// options builds the answers on offer: allow once (remembering nothing), for this session, always —
// each on the action's EXACT target — then the tool's suggested widenings, each always-remembered.
// The order is the presentation order and matches internal/hitl, so the same action reads the same
// way whether it is answered here or on the phone.
//
// The ceiling drops what the gate would narrow anyway, through the same gate.Offerable the phone's
// sheet uses — a person at a terminal must not be offered "always" for a kind that is asked every
// time either. A widening's LABEL still says "always", which is why an unofferable one is dropped
// rather than relabelled: "always net api.example.com, for this call only" is not a button.
func options(a gate.Action, ceiling gate.Recall, suggest []gate.Grant) []Option {
	exact := gate.Grant{Kind: a.Kind, Target: a.Target}
	var opts []Option
	add := func(lbl string, want gate.Recall, g gate.Grant, widens bool) {
		recall, ok := gate.Offerable(want, ceiling, widens)
		if !ok {
			return
		}
		opts = append(opts, Option{Label: lbl, Grant: g, Recall: recall, Widens: widens})
	}
	answer := func(lbl string, want gate.Recall) { add(lbl, want, exact, false) }
	widening := func(g gate.Grant) { add("always "+label(g), gate.RecallAlways, g, true) }

	answer("once", gate.RecallNever)
	answer("this session", gate.RecallSession)
	answer("always", gate.RecallAlways)
	for _, s := range suggest {
		widening(s)
	}
	return opts
}

// label renders a grant for the modal. It reads gate types only — never a tool's arguments — so an
// injected prompt cannot phrase the question it is being asked about.
func label(g gate.Grant) string {
	if g.Target == "" {
		return g.Kind
	}
	return g.Kind + " " + strconv.Quote(g.Target)
}
