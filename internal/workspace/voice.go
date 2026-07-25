package workspace

import (
	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/tools"
	"github.com/efuturetoday/nocturn/internal/voice"
)

// voiceCage is the tool set a spoken session may reach. It is an ALLOWLIST, and everything on it is
// read-only or self-addressed: nothing here changes the world outside this workspace.
//
// The reasoning is the microphone. A speaker in a room takes instructions from whoever is audible —
// the household, a guest, a television, an assistant on someone else's phone. None of them are
// authenticated, and unlike a typed chat there is no moment where a human deliberately submits.
// So the boundary is drawn by absence: a writing tool is not bound into the session at all, which
// is stronger than a policy that could deny it, because a tool that is never declared cannot be
// named, hallucinated into, or talked around.
//
// Adding a writing tool here is a security decision, not a feature toggle. The path for one is an
// out-of-band confirmation on the phone (see the driver's approver), never a widening of this list.
var voiceCage = map[string]bool{
	"file_list":   true,
	"file_read":   true,
	"file_search": true,
	"file_stat":   true,
	"http_read":   true,
	"dns_resolve": true,
	"ping":        true,
	"remind":      true,
	"remind_list": true,
	"skill_read":  true,
	"time_now":    true,
	"notify":      true,
}

// voicePolicy is the gate policy for a spoken session, and it deliberately differs from policy():
// the read kinds are ALLOWED rather than asked.
//
// That reads like a loosening and is the opposite. policy() asks on NetKind and FileKind because a
// typed session can reach write tools through the same kinds; a voice session cannot, because
// voiceCage never binds them. The cage carries the restriction, so the policy carrying it a second
// time would only produce approval prompts mid-sentence — which in practice trains a user to
// approve reflexively, and a reflexive approver is worse than none.
//
// Everything outside the read kinds still denies. A tool that somehow reached this policy without
// being in the cage is a bug, and it fails closed rather than defaulting to allow.
// The kinds a caged voice session may use at all. Anything else denies.
func voiceAllowed(kind string) bool {
	switch kind {
	case tools.NetKind, tools.FileKind, tools.NotifyKind, tools.RemindKind:
		return true
	}
	return false
}

func voicePolicy(ask map[string]bool) gate.Policy {
	return gate.PolicyFunc(func(a gate.Action) gate.Ruling {
		switch {
		case !voiceAllowed(a.Kind):
			return gate.Denied()
		case ask[a.Kind]:
			// RecallNever, not RecallSession: this path exists to be felt repeatedly within one
			// conversation. A remembered yes would answer the first ask and hide every one after it,
			// which is the opposite of what a measurement wants to observe.
			return gate.AskWith(gate.RecallNever)
		default:
			return gate.Allowed()
		}
	})
}

// VoiceOption configures how a voice session is composed.
type VoiceOption func(*voiceConfig)

type voiceConfig struct {
	approver gate.Approver
	ask      map[string]bool
	driver   []voice.Option
}

// VoiceApprover routes a voice session's asks to a human. The default is none, which is the
// unattended posture a screenless satellite runs in: an Ask with no covering grant fails closed.
//
// Passing one is only meaningful together with VoiceAsk — with the shipped policy nothing in the
// cage ever asks.
func VoiceApprover(a gate.Approver) VoiceOption {
	return func(c *voiceConfig) { c.approver = a }
}

// VoiceAsk switches the named gate kinds from allow to ask.
//
// This is a MEASUREMENT instrument, not a configuration: it exists to find out what an approval
// actually feels like in the middle of a spoken sentence — how long the silence is, what the model
// says while it waits, whether a user talks over it — before that shape is committed to. The
// shipped posture asks nothing, because the cage already carries the restriction and asking every
// sentence teaches people to approve without reading.
//
// Unknown kinds are accepted and simply never match; the caller is experimenting, not configuring.
func VoiceAsk(kinds ...string) VoiceOption {
	return func(c *voiceConfig) {
		if c.ask == nil {
			c.ask = make(map[string]bool, len(kinds))
		}
		for _, k := range kinds {
			c.ask[k] = true
		}
	}
}

// VoiceDriver passes options through to the driver (budget, observer, …).
func VoiceDriver(opts ...voice.Option) VoiceOption {
	return func(c *voiceConfig) { c.driver = append(c.driver, opts...) }
}

// voiceRider is appended to the workspace persona for spoken sessions. A persona written for a
// screen does not survive contact with a speaker: there is no formatting, no scrollback, and no
// visible spinner telling the user that something is happening.
//
// The waiting instruction is the load-bearing one. Declaring tools non-blocking lets the model keep
// talking while a call is outstanding — but it only MAY, it does not have to, and with nothing else
// pending it simply goes quiet. Silence during an approval is the failure mode that teaches people
// to grant permanently just to make it stop, so the model is told to fill it and to name what it is
// waiting on.
const voiceRider = `
You are speaking out loud, not writing. Keep replies short and plain — no lists, no markdown, no
formatting of any kind. Say numbers, dates and units the way a person would say them.

A line starting with "[system]" is not the person speaking. It is a fact about what is happening
right now that you could not otherwise know. Use it, but never read it out verbatim.

When something you asked for has not come back yet, mention it ONCE, briefly, and then let it go.
Do not guess why it is taking time — if there is a reason you need to pass on, a [system] line will
tell you. Do not repeat that you are still waiting; the person heard you the first time. Carry on
with whatever they want to talk about, and report the outcome when it arrives.

If something is refused, say so plainly and offer what you can still do.`

// Voice builds a driver for a spoken session over live, caged by voiceCage and gated by
// voicePolicy. It shares this workspace's durable grants and its persona, so a voice conversation
// is the same assistant the terminal and the app talk to — reachable through a narrower door.
//
// With no options it is the shipped posture: nothing asks, and there is no approver, so an Ask that
// somehow escaped the cage has no one to answer it and fails closed.
func (w *Workspace) Voice(live agentkit.LiveLLM, opts ...VoiceOption) *voice.Driver {
	var cfg voiceConfig
	for _, o := range opts {
		o(&cfg)
	}
	caged := w.tools.Select(func(name string) bool { return voiceCage[name] })
	driver := append([]voice.Option{
		voice.WithSystem(resolvePersona(w.dir, w.log) + "\n" + voiceRider),
		voice.WithLogger(w.log.With("component", "voice")),
	}, cfg.driver...)
	return voice.New(live, caged, voicePolicy(cfg.ask), w.grants, cfg.approver, driver...)
}

// VoiceTools reports the caged tool names, sorted, so a caller can show the user what a spoken
// session is actually able to do. An always-on microphone should not have to be taken on trust.
func (w *Workspace) VoiceTools() []string {
	var names []string
	for _, spec := range w.tools.Select(func(name string) bool { return voiceCage[name] }).Specs() {
		names = append(names, spec.Name)
	}
	return names
}
