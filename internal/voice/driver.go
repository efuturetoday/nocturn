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
	"sync/atomic"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/speaker"
)

// DefaultBudget bounds one session's wall clock. A live model bills per audio minute, so a session
// nobody hung up is a running cost — unlike a stalled turn, which simply sits there. Fail closed on
// time, not on the user remembering to disconnect.
const DefaultBudget = 15 * time.Minute

// DefaultIdle is how long a conversation waits after a reply before hanging up on its own.
//
// Two scenarios pull in opposite directions and this is where they meet. "What time is it" wants the
// session shut immediately afterwards, or the device sits there listening to the room and the model
// answers half-sentences it was never asked. "What's on today" — "and tomorrow?" wants it open, or
// every follow-up needs the wake word again.
//
// So: open long enough for a follow-up, closed before it becomes an open microphone. Alexa and
// Google settled on a few seconds for the same reason.
//
// It is measured from LiveTurnDone, which is the model finishing GENERATION — the device may still
// have a couple of seconds of it queued. The window therefore covers both, and the silence a person
// actually experiences is shorter than the number. Waiting for the device to report itself drained
// would be exact and needs a round trip to learn something this can simply absorb.
const DefaultIdle = 10 * time.Second

// hangUp is how the model ends a conversation, and it is NOT one of the caged tools.
//
// It steers the session rather than reaching into the world, which is a different kind of thing:
// there is nothing here to permit or deny, and a gate check would be asking whether the assistant may
// stop listening to you. It is answered by the driver directly and never enters the ToolSet.
//
// It exists because the alternative endings are both worse manners. Waiting out the silence window
// leaves ten seconds of dead air after "goodbye"; waiting out the budget leaves fifteen minutes of
// an open microphone. A person who says they are done should be taken at their word.
const hangUp = "hang_up"

var hangUpSpec = agentkit.ToolSpec{
	Name: hangUp,
	Description: "End the spoken conversation. Call this when the person has said goodbye, said " +
		"they are done, or otherwise signalled that the exchange is over. The microphone stops " +
		"listening until they say the wake word again.",
}

// Device is the satellite end of a session: a microphone, a speaker, and the control signals a
// duplex conversation needs. A browser tab and an ESP32 implement the same handful of methods, which
// is why the PoC client is not throwaway work.
type Device interface {
	// Recv blocks for the next chunk of microphone PCM. It returns an error when the device
	// disconnects, which ends the session.
	Recv(ctx context.Context) ([]byte, error)
	// Play queues one chunk of model speech for the speaker.
	Play(pcm []byte) error
	// Interrupt tells the device to DROP whatever it has buffered but not yet played. Without it a
	// barge-in still lets the speaker finish answering a question the user already abandoned.
	Interrupt() error
	// Heard fires when the DEVICE's own voice detector picks up a voice — not when the model has
	// understood something. It is what tells a conversation that somebody is still there.
	//
	// It comes from the device because that is where the detector is, running on the echo-cancelled
	// microphone signal about 200 ms after a person starts. The model's transcript arrives later and
	// only for speech it made sense of, which is the wrong test for "is anyone still in the room".
	Heard() <-chan struct{}
	// Waiting reports that the conversation is blocked on a human decision somewhere else — or that
	// it no longer is.
	//
	// Everything else a device shows it derives from what it observed itself, because a round trip is
	// slower than a person's sense of immediacy. This is the exception: an approval is happening on a
	// phone the device knows nothing about, and from where it stands the conversation merely stopped.
	Waiting(on bool) error
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

	system   func(speaker string) string
	budget   time.Duration
	idle     time.Duration
	observer Observer
	log      *slog.Logger
}

// Option configures a Driver.
type Option func(*Driver)

// WithIdle overrides DefaultIdle. A non-positive value disables hanging up on silence, which leaves
// only the budget — deliberately possible, and deliberately not the default.
func WithIdle(t time.Duration) Option { return func(d *Driver) { d.idle = t } }

// WithSystemFunc sets what builds the system instruction, called once per session.
//
// A function rather than a string because the prompt is not a constant: it carries the memory index,
// which changes as the assistant writes notes, and it names the speaker when one is recognised. A
// string fixed when the workspace was assembled would hand every session the state of the day the
// daemon started.
//
// Once per session and not more often is not a choice: a live session's system instruction travels
// in the setup frame and cannot be replaced afterwards. Anything the model needs to learn mid-
// conversation has to be something it asks for — see the whoami tool.
func WithSystemFunc(f func(speaker string) string) Option {
	return func(d *Driver) { d.system = f }
}

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
		idle:     DefaultIdle,
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
func (d *Driver) Run(ctx context.Context, dev Device, conv []agentkit.Message, who func() speaker.Identity) ([]agentkit.Message, error) {
	if who == nil {
		who = func() speaker.Identity { return speaker.Identity{} } // no microphone knows anybody
	}
	// Covers the early return below; the normal path cancels explicitly in its teardown closure, and
	// a second cancel is a no-op.
	ctx, cancel := context.WithTimeout(ctx, d.budget)
	defer cancel()

	seed := conv
	if d.system != nil {
		if prompt := d.system(who().Name); prompt != "" {
			seed = append([]agentkit.Message{{Role: agentkit.RoleSystem, Content: prompt}}, conv...)
		}
	}
	sess, err := d.live.Open(ctx, seed, append(d.tools.Specs(), hangUpSpec))
	if err != nil {
		return conv, fmt.Errorf("voice: open session: %w", err)
	}

	// The gate machinery rides on the ctx every tool Call runs under. This one install covers the
	// whole session: each tool's own gate.Check finds it here, exactly as it does inside a turn.
	// The approver is decorated so the conversation is told WHY it is about to wait.
	// The speaker rides here so a tool can act on the right person's behalf without the model having
	// to name them — and without being able to name the wrong one.
	toolCtx := speaker.NewContext(
		gate.WithLogger(gate.With(ctx, d.policy, d.grants, d.announcing(sess, dev)), agentkit.SlogLogger(d.log)),
		who)

	// The microphone pump is its own goroutine: it blocks on the device, and the event loop below
	// blocks on the model. Neither may wait for the other, or the conversation deadlocks. It exits
	// when the device errors or ctx ends, and its channel is buffered, so it can always finish its
	// send even if Run has already returned.
	uplink := make(chan error, 1)
	go func() { uplink <- d.pump(ctx, dev, sess) }()

	c := &conversation{dev: dev, sess: sess, tr: newTranscript(conv)}

	// Teardown order is load-bearing, so it is spelled out rather than left to defer stacking:
	// cancel FIRST, so a tool still blocked on a human approval is released immediately — waiting
	// on it first would hold the session open for as long as the approval takes, up to the whole
	// budget, long after the caller hung up.
	defer func() {
		cancel()
		c.pending.Wait()
		sess.Close()
	}()

	// The silence timer. Stopped means nothing is counting: it runs only between a finished reply and
	// the next sign of life, so a conversation is never hung up while anything is still happening.
	idle := time.NewTimer(d.budget)
	idle.Stop()
	defer idle.Stop()

	for {
		select {
		case <-ctx.Done():
			// A session that ran out its budget ended normally — it is a cost guard, not a failure —
			// but it must be visible, or a conversation that died on the clock looks like a device bug.
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				d.log.Info("voice session ended on its budget", "budget", d.budget)
			}
			return c.tr.messages(), nil
		case err := <-uplink:
			return c.tr.messages(), err
		case <-dev.Heard():
			// Somebody is still there. This is the device's own detector, so it arrives while a person
			// is drawing breath rather than after the model has understood a sentence.
			c.stopIdle(idle)
		case <-idle.C:
			d.log.Info("voice session ended on silence", "idle", d.idle)
			return c.tr.messages(), nil
		case ev, ok := <-sess.Events():
			if !ok {
				return c.tr.messages(), nil
			}
			if err := d.handle(toolCtx, c, ev); err != nil {
				if errors.Is(err, errHungUp) {
					return c.tr.messages(), nil
				}
				return c.tr.messages(), err
			}
			d.armIdle(c, idle, ev)
		}
	}
}

// armIdle starts or stops the silence timer according to what just happened.
//
// Only a finished reply starts it. Everything else stops it, and the two cases worth naming are the
// ones that stop it for a LONG time: a tool call, which may be doing something slow, and an approval,
// which is waiting on a person walking to another room. Hanging up on either would be hanging up
// precisely when the conversation is most valuable — and, in the approval case, when the human has
// already been asked to do something.
func (d *Driver) armIdle(c *conversation, idle *time.Timer, ev agentkit.LiveEvent) {
	if d.idle <= 0 {
		return
	}
	switch ev.(type) {
	case agentkit.LiveTurnDone:
		// Not while a tool is still in flight: the model has finished SPEAKING, and the reply that
		// matters may still be waiting on what the tool returns.
		if c.running() > 0 {
			return
		}
		c.stopIdle(idle)
		idle.Reset(d.idle)
	default:
		c.stopIdle(idle)
	}
}

// stopIdle stops the timer and drains it, so a fire that raced the stop does not end the next
// silence early.
func (c *conversation) stopIdle(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}

// conversation is one session's mutable state, bound together so the event loop and the tool
// goroutines share it by reference rather than by an ever-growing parameter list.
type conversation struct {
	dev  Device
	sess agentkit.LiveSession
	tr   *transcript

	pending sync.WaitGroup
	// turns completed so far: written by the event loop, read by each tool goroutine when it
	// answers. It is how a call learns the conversation moved on without it.
	turns atomic.Uint64

	// inflight holds one cancel per running call, so the provider withdrawing a call can stop the
	// work it started. Written by the event loop and by each tool goroutine as it finishes.
	mu       sync.Mutex
	inflight map[string]context.CancelFunc
}

// running is how many calls are in flight. The silence timer needs to ask, and a WaitGroup can only
// be waited on: a reply that finished while a tool is still working is not the end of anything.
func (c *conversation) running() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.inflight)
}

// start registers a call and returns the ctx its work runs under.
func (c *conversation) start(ctx context.Context, id string) context.Context {
	ctx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inflight == nil {
		c.inflight = map[string]context.CancelFunc{}
	}
	c.inflight[id] = cancel
	return ctx
}

// finish releases a call's registration, cancelling it so the context is never leaked.
func (c *conversation) finish(id string) {
	c.mu.Lock()
	cancel := c.inflight[id]
	delete(c.inflight, id)
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// cancel stops the named calls and reports which were actually running.
func (c *conversation) cancel(ids []string) []string {
	c.mu.Lock()
	var hit []string
	for _, id := range ids {
		if cancel, ok := c.inflight[id]; ok {
			delete(c.inflight, id)
			hit = append(hit, id)
			defer cancel()
		}
	}
	c.mu.Unlock()
	return hit
}

// handle applies one live event. It returns an error only for conditions that end the session.
func (d *Driver) handle(ctx context.Context, c *conversation, ev agentkit.LiveEvent) error {
	d.trace(ev)
	switch e := ev.(type) {
	case agentkit.LiveCallsCancelled:
		// The provider has withdrawn these calls, so their work is now unwanted — including any
		// approval still sitting on somebody's phone, which must not grant authority for a call that
		// no longer exists. Cancelling the ctx unwinds the tool and the gate check together.
		if hit := c.cancel(e.IDs); len(hit) > 0 {
			d.log.Info("voice calls cancelled by the provider", "ids", hit)
		}
	case agentkit.LiveAudio:
		if err := c.dev.Play(e.PCM); err != nil {
			return fmt.Errorf("voice: play: %w", err)
		}
	case agentkit.LiveInterrupted:
		// Drop the device's buffer first, then the half-spoken sentence: the user has moved on, and
		// keeping either would have the speaker answer a question that no longer stands.
		if err := c.dev.Interrupt(); err != nil {
			return fmt.Errorf("voice: interrupt: %w", err)
		}
		c.tr.discardPartial()
	case agentkit.LiveUserText:
		c.tr.append(agentkit.RoleUser, e.Text)
	case agentkit.LiveModelText:
		c.tr.append(agentkit.RoleAssistant, e.Text)
	case agentkit.LiveTurnDone:
		c.turns.Add(1)
		for _, m := range c.tr.commit() {
			if d.observer != nil {
				d.observer.Said(m.Role, m.Content)
			}
		}
	case agentkit.LiveToolCall:
		if e.Tool == hangUp {
			// Answered before ending, so the provider is not left with a call it never got a result
			// for — and answered synchronously, because there is nothing to do and the session is
			// about to go away underneath any goroutine that tried.
			_ = c.sess.SendResult(ctx, agentkit.ToolResult{ID: e.ID, Tool: e.Tool, Result: "ended"})
			d.log.Info("voice session ended by the model")
			return errHungUp
		}
		// Concurrent on purpose: a gated call can block for as long as a human takes to answer on
		// another device. Running it inline would stall the audio path — including the model's own
		// "hold on, I need permission for that".
		issued := c.turns.Load()
		c.pending.Go(func() { d.invoke(ctx, c, e, issued) })
	case agentkit.LiveError:
		return fmt.Errorf("voice: session: %w", e.Err)
	}
	return nil
}

// invoke runs one tool call and answers it. Every outcome — unknown tool, gate denial, tool failure
// — is reported back to the model as a result, because a call left unanswered hangs the
// conversation, and a denial is something the model should say out loud rather than stall on.
func (d *Driver) invoke(ctx context.Context, c *conversation, call agentkit.LiveToolCall, issued uint64) {
	ctx = c.start(withCall(ctx, call), call.ID)
	defer c.finish(call.ID)
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
	// A withdrawn call wants no answer, and a session that is shutting down cannot deliver one.
	// Reporting either way would address an id the provider has already forgotten.
	if ctx.Err() != nil {
		d.log.Debug("voice tool result dropped", "tool", call.Tool, "id", call.ID, "err", ctx.Err())
		return
	}
	// A turn completed while this call was outstanding, so the person has moved on and the answer
	// must wait for a gap rather than cut into whatever they are talking about now.
	res.Late = c.turns.Load() > issued
	if err := c.sess.SendResult(ctx, res); err != nil {
		d.log.Warn("voice tool result undeliverable", "tool", call.Tool, "err", err)
	}
}

// callKey carries the id of the tool call a gate check belongs to. The gate hands an approver only
// the Action — a kind and a target — but answering the model requires the call's id, and the only
// layer that knows both is the one that dispatched the call.
type callKey struct{}

func withCall(ctx context.Context, call agentkit.LiveToolCall) context.Context {
	return context.WithValue(ctx, callKey{}, call)
}

// announcing wraps the approver so that every ask first tells the model what is pending.
// It returns nil unchanged when there is no approver — the unattended posture has nothing to
// announce, because nobody is being waited for.
func (d *Driver) announcing(sess agentkit.LiveSession, dev Device) gate.Approver {
	if d.approver == nil {
		return nil
	}
	return &announcingApprover{inner: d.approver, sess: sess, dev: dev, log: d.log}
}

// announcingApprover states the reason for a wait before blocking on it.
//
// Only this layer knows the reason. From the model's side a pending call is opaque — it sees that
// it called something and that nothing came back, and cannot tell a slow server from a human
// holding a phone. Left to guess, an assistant asked to explain the wait would sometimes send
// people to a device where nothing is pending, which is worse than silence.
//
// It answers the CALL rather than speaking into the conversation. Injected text counts as somebody
// talking, so it interrupts the model mid-sentence and makes it abandon what it was saying — the
// exact rudeness the announcement exists to prevent. An interim result is data about a call the
// model already made, and arrives without anyone appearing to speak.
type announcingApprover struct {
	inner gate.Approver
	sess  agentkit.LiveSession
	dev   Device
	log   *slog.Logger
}

var _ gate.Approver = (*announcingApprover)(nil)

func (a *announcingApprover) Ask(ctx context.Context, act gate.Action, ceiling gate.Recall, suggest []gate.Grant) (bool, gate.Grant, gate.Recall, error) {
	call, ok := ctx.Value(callKey{}).(agentkit.LiveToolCall)
	if !ok {
		// A gate check outside a live tool call — nothing to answer, so nothing to announce.
		return a.inner.Ask(ctx, act, ceiling, suggest)
	}
	target := act.Kind
	if act.Target != "" {
		target += " " + act.Target
	}
	interim := agentkit.ToolResult{
		ID: call.ID, Tool: call.Tool, Pending: true,
		Result: "Waiting for the person to approve this on their paired device: " + target +
			". Tell them once, briefly, then carry on.",
	}
	// A failed announcement must not stop the approval: the wait is the important part, the
	// narration is not. It is logged and the ask proceeds.
	if err := a.sess.SendResult(ctx, interim); err != nil {
		a.log.Warn("voice: could not announce a pending approval", "err", err)
	}

	// And show it. The narration above reaches whoever is listening; this reaches whoever walks past
	// the device and wonders why it went quiet. Failing to show it must not stop the approval either.
	if err := a.dev.Waiting(true); err != nil {
		a.log.Warn("voice: could not show a pending approval", "err", err)
	}
	defer func() {
		if err := a.dev.Waiting(false); err != nil {
			a.log.Warn("voice: could not clear a pending approval", "err", err)
		}
	}()

	return a.inner.Ask(ctx, act, ceiling, suggest)
}

// trace logs one live event at debug level. A duplex session is otherwise opaque from the outside:
// whether the provider ever sent an interruption, how a turn was punctuated, and whether calls get
// withdrawn are all questions that can only be answered by watching the stream, and guessing at them
// costs more than the log line does.
func (d *Driver) trace(ev agentkit.LiveEvent) {
	if !d.log.Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	switch e := ev.(type) {
	case agentkit.LiveAudio:
		d.log.Debug("live audio", "bytes", len(e.PCM))
	case agentkit.LiveUserText:
		d.log.Debug("live user text", "text", e.Text)
	case agentkit.LiveModelText:
		d.log.Debug("live model text", "text", e.Text)
	case agentkit.LiveToolCall:
		d.log.Debug("live tool call", "id", e.ID, "tool", e.Tool, "args", e.Args)
	case agentkit.LiveCallsCancelled:
		d.log.Debug("live calls cancelled", "ids", e.IDs)
	case agentkit.LiveInterrupted:
		d.log.Debug("live interrupted")
	case agentkit.LiveTurnDone:
		d.log.Debug("live turn done")
	case agentkit.LiveError:
		d.log.Debug("live error", "err", e.Err)
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
