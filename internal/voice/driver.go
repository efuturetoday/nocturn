// Package voice drives a duplex speech session against an agentkit.LiveLLM, brokering the tool
// calls it issues through the same ToolSet and the same gate machinery a typed chat uses.
//
// It exists because agentkit.Session cannot: Session is a turn loop, and a live conversation has no
// turns. What Session provides on the way — transcript persistence, an event stream, a spend budget
// — this driver re-provides in the shapes a stream needs (a transcript flushed at each spoken
// reply, a wall-clock budget instead of a token budget).
//
// The security posture is unchanged, and deliberately so: the tools come from a ToolSet the caller
// caged, the gate machinery is installed on the ctx every Call runs under, and each tool still
// performs its own gate.Check with its own Kind and Target. Speech is a new way to REACH the tools,
// never a way around them.
package voice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
)

// DefaultBudget bounds one session's wall clock. A live model bills per audio minute, so a session
// nobody hung up is a running cost — unlike a stalled turn, which simply sits there. Fail closed on
// time, not on the user remembering to disconnect.
const DefaultBudget = 15 * time.Minute

// Device is the satellite end of a session: a microphone, a speaker, and the one control signal a
// duplex conversation needs. A browser tab and an ESP32 implement the same three methods, which is
// why the PoC client is not throwaway work.
type Device interface {
	// Recv blocks for the next chunk of microphone PCM. It returns an error when the device
	// disconnects, which ends the session.
	Recv(ctx context.Context) ([]byte, error)
	// Play queues one chunk of model speech for the speaker.
	Play(pcm []byte) error
	// Interrupt tells the device to DROP whatever it has buffered but not yet played. Without it a
	// barge-in still lets the speaker finish answering a question the user already abandoned.
	Interrupt() error
}

// Observer watches a session without steering it — for logging, for showing the phone what the
// speaker in the other room is doing. A nil Observer is fine; every method is optional.
//
// Making tool activity visible is not decoration: an always-on microphone that acts on the
// household's behalf should be auditable from a screen the user actually holds.
//
// An implementation must be safe for concurrent use: Said comes from the event loop while ToolRan
// comes from each tool's own goroutine, and several tools can be in flight at once.
type Observer interface {
	Said(role agentkit.Role, text string)
	ToolRan(name, args, result string, err error)
}

// Driver holds everything a session needs and is safe to reuse across sessions: Run builds all
// per-session state locally.
type Driver struct {
	live     agentkit.LiveLLM
	tools    agentkit.ToolSet
	policy   gate.Policy
	grants   gate.Grants
	approver gate.Approver

	system   string
	budget   time.Duration
	observer Observer
	log      *slog.Logger
}

// Option configures a Driver.
type Option func(*Driver)

// WithSystem sets the persona handed to the model as its system instruction.
func WithSystem(s string) Option { return func(d *Driver) { d.system = s } }

// WithBudget overrides DefaultBudget. A non-positive value is ignored — there is no "unlimited",
// because unlimited is the failure mode this budget exists to prevent.
func WithBudget(t time.Duration) Option {
	return func(d *Driver) {
		if t > 0 {
			d.budget = t
		}
	}
}

// WithObserver attaches a session watcher. A nil observer is ignored.
func WithObserver(o Observer) Option {
	return func(d *Driver) {
		if o != nil {
			d.observer = o
		}
	}
}

// WithLogger sets the driver's logger. A nil logger is ignored.
func WithLogger(l *slog.Logger) Option {
	return func(d *Driver) {
		if l != nil {
			d.log = l
		}
	}
}

// New builds a Driver over live, exposing tools gated by policy/grants/approver.
//
// The caller owns the cage: whichever tools are in the set are the tools a voice session can reach,
// and a tool that is absent cannot be denied because it cannot be named. A nil approver is the
// unattended posture — an Ask with no covering grant then fails closed, which is the right default
// for a device with no screen.
func New(live agentkit.LiveLLM, tools agentkit.ToolSet, policy gate.Policy, grants gate.Grants, approver gate.Approver, opts ...Option) *Driver {
	d := &Driver{
		live:     live,
		tools:    tools,
		policy:   policy,
		grants:   grants,
		approver: approver,
		budget:   DefaultBudget,
		log:      slog.Default(),
	}
	for _, o := range opts {
		o(d)
	}
	return d
}

// Run holds one conversation with dev, seeded with conv, until the device disconnects, the budget
// expires, or ctx is cancelled. It returns the transcript of what was said — the caller decides
// where that goes, so the driver needs no store.
func (d *Driver) Run(ctx context.Context, dev Device, conv []agentkit.Message) ([]agentkit.Message, error) {
	// Covers the early return below; the normal path cancels explicitly in its teardown closure, and
	// a second cancel is a no-op.
	ctx, cancel := context.WithTimeout(ctx, d.budget)
	defer cancel()

	seed := conv
	if d.system != "" {
		seed = append([]agentkit.Message{{Role: agentkit.RoleSystem, Content: d.system}}, conv...)
	}
	sess, err := d.live.Open(ctx, seed, d.tools.Specs())
	if err != nil {
		return conv, fmt.Errorf("voice: open session: %w", err)
	}

	// The gate machinery rides on the ctx every tool Call runs under. This one install covers the
	// whole session: each tool's own gate.Check finds it here, exactly as it does inside a turn.
	toolCtx := gate.WithLogger(gate.With(ctx, d.policy, d.grants, d.approver), agentkit.SlogLogger(d.log))

	// The microphone pump is its own goroutine: it blocks on the device, and the event loop below
	// blocks on the model. Neither may wait for the other, or the conversation deadlocks. It exits
	// when the device errors or ctx ends, and its channel is buffered, so it can always finish its
	// send even if Run has already returned.
	uplink := make(chan error, 1)
	go func() { uplink <- d.pump(ctx, dev, sess) }()

	var pending sync.WaitGroup
	transcript := newTranscript(conv)

	// Teardown order is load-bearing, so it is spelled out rather than left to defer stacking:
	// cancel FIRST, so a tool still blocked on a human approval is released immediately — waiting
	// on it first would hold the session open for as long as the approval takes, up to the whole
	// budget, long after the caller hung up.
	defer func() {
		cancel()
		pending.Wait()
		sess.Close()
	}()

	for {
		select {
		case <-ctx.Done():
			// A session that ran out its budget ended normally — it is a cost guard, not a failure —
			// but it must be visible, or a conversation that died on the clock looks like a device bug.
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				d.log.Info("voice session ended on its budget", "budget", d.budget)
			}
			return transcript.messages(), nil
		case err := <-uplink:
			return transcript.messages(), err
		case ev, ok := <-sess.Events():
			if !ok {
				return transcript.messages(), nil
			}
			if err := d.handle(toolCtx, dev, sess, transcript, &pending, ev); err != nil {
				return transcript.messages(), err
			}
		}
	}
}

// handle applies one live event. It returns an error only for conditions that end the session.
func (d *Driver) handle(ctx context.Context, dev Device, sess agentkit.LiveSession, tr *transcript, pending *sync.WaitGroup, ev agentkit.LiveEvent) error {
	switch e := ev.(type) {
	case agentkit.LiveAudio:
		if err := dev.Play(e.PCM); err != nil {
			return fmt.Errorf("voice: play: %w", err)
		}
	case agentkit.LiveInterrupted:
		// Drop the device's buffer first, then the half-spoken sentence: the user has moved on, and
		// keeping either would have the speaker answer a question that no longer stands.
		if err := dev.Interrupt(); err != nil {
			return fmt.Errorf("voice: interrupt: %w", err)
		}
		tr.discardPartial()
	case agentkit.LiveUserText:
		tr.append(agentkit.RoleUser, e.Text)
	case agentkit.LiveModelText:
		tr.append(agentkit.RoleAssistant, e.Text)
	case agentkit.LiveTurnDone:
		for _, m := range tr.commit() {
			if d.observer != nil {
				d.observer.Said(m.Role, m.Content)
			}
		}
	case agentkit.LiveToolCall:
		// Concurrent on purpose: a gated call can block for as long as a human takes to answer on
		// another device. Running it inline would stall the audio path — including the model's own
		// "hold on, I need permission for that".
		pending.Go(func() { d.invoke(ctx, sess, e) })
	case agentkit.LiveError:
		return fmt.Errorf("voice: session: %w", e.Err)
	}
	return nil
}

// invoke runs one tool call and answers it. Every outcome — unknown tool, gate denial, tool failure
// — is reported back to the model as a result, because a call left unanswered hangs the
// conversation, and a denial is something the model should say out loud rather than stall on.
func (d *Driver) invoke(ctx context.Context, sess agentkit.LiveSession, call agentkit.LiveToolCall) {
	res := agentkit.ToolResult{ID: call.ID, Tool: call.Tool}
	tool, ok := d.tools[call.Tool]
	if !ok {
		// Not reachable through a well-behaved model, but a hallucinated name must not be fatal.
		res.Err = fmt.Errorf("unknown tool %q", call.Tool)
	} else {
		out, err := tool.Call(ctx, call.Args)
		res.Result, res.Err = out, err
	}
	if d.observer != nil {
		d.observer.ToolRan(call.Tool, call.Args, res.Result, res.Err)
	}
	switch {
	case res.Err != nil && errors.Is(res.Err, gate.ErrDenied):
		d.log.Info("voice tool denied", "tool", call.Tool, "err", res.Err)
	case res.Err != nil:
		d.log.Warn("voice tool failed", "tool", call.Tool, "err", res.Err)
	}
	if err := sess.SendResult(ctx, res); err != nil {
		d.log.Warn("voice tool result undeliverable", "tool", call.Tool, "err", err)
	}
}

// pump forwards microphone audio upstream until the device disconnects or ctx ends.
func (d *Driver) pump(ctx context.Context, dev Device, sess agentkit.LiveSession) error {
	for {
		pcm, err := dev.Recv(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil // the session ended around us; not the device's fault
			}
			return fmt.Errorf("voice: device: %w", err)
		}
		if err := sess.SendAudio(ctx, pcm); err != nil {
			return fmt.Errorf("voice: uplink: %w", err)
		}
	}
}

// transcript accumulates the provider's streamed transcription into whole messages. Deltas arrive
// mid-word, so text is joined and only committed at a turn boundary.
type transcript struct {
	done    []agentkit.Message
	partial map[agentkit.Role]*strings.Builder
	order   []agentkit.Role
}

func newTranscript(seed []agentkit.Message) *transcript {
	return &transcript{
		done:    append([]agentkit.Message(nil), seed...),
		partial: map[agentkit.Role]*strings.Builder{},
	}
}

func (t *transcript) append(role agentkit.Role, text string) {
	b, ok := t.partial[role]
	if !ok {
		b = &strings.Builder{}
		t.partial[role] = b
		t.order = append(t.order, role)
	}
	b.WriteString(text)
}

// commit closes the open messages in the order their first delta arrived — the user spoke before
// the model answered, and a transcript that lost that order would read backwards.
func (t *transcript) commit() []agentkit.Message {
	var out []agentkit.Message
	for _, role := range t.order {
		if text := strings.TrimSpace(t.partial[role].String()); text != "" {
			out = append(out, agentkit.Message{Role: role, Content: text})
		}
	}
	t.done = append(t.done, out...)
	t.partial = map[agentkit.Role]*strings.Builder{}
	t.order = nil
	return out
}

// discardPartial drops the half-spoken model reply on a barge-in while KEEPING what the user said —
// the interruption cut the answer, not the question.
func (t *transcript) discardPartial() {
	delete(t.partial, agentkit.RoleAssistant)
	for i, r := range t.order {
		if r == agentkit.RoleAssistant {
			t.order = append(t.order[:i], t.order[i+1:]...)
			break
		}
	}
}

func (t *transcript) messages() []agentkit.Message {
	t.commit()
	return t.done
}
