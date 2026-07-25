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
func voicePolicy() gate.Policy {
	return gate.PolicyFunc(func(a gate.Action) gate.Ruling {
		switch a.Kind {
		case tools.NetKind, tools.FileKind, tools.NotifyKind, tools.RemindKind:
			return gate.Allowed()
		default:
			return gate.Denied()
		}
	})
}

// Voice builds a driver for a spoken session over live, caged by voiceCage and gated by
// voicePolicy. It shares this workspace's durable grants and its persona, so a voice conversation
// is the same assistant the terminal and the app talk to — reachable through a narrower door.
//
// The approver is deliberately absent: a satellite has no authenticated input path, so an Ask that
// escaped the cage has no one to answer it and fails closed.
func (w *Workspace) Voice(live agentkit.LiveLLM, opts ...voice.Option) *voice.Driver {
	caged := w.tools.Select(func(name string) bool { return voiceCage[name] })
	base := []voice.Option{
		voice.WithSystem(resolvePersona(w.dir, w.log)),
		voice.WithLogger(w.log.With("component", "voice")),
	}
	return voice.New(live, caged, voicePolicy(), w.grants, nil, append(base, opts...)...)
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
