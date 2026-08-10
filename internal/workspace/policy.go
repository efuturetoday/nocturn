package workspace

import (
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/memory"
	"github.com/efuturetoday/nocturn/internal/tools"
)

// This file is the workspace permission model: which Kind of action runs free, which one asks a
// human, and how that answer may be remembered. It is small on purpose and it is its own file on
// purpose — it is the rule CLAUDE.md §4 describes, and tightening it is a deliberate change rather
// than a bugfix, so it should not have to be found underneath something else.

// policy is the workspace-root policy — the one a chat the human is watching runs under: the net and
// file kinds ask; every other Kind runs free.
//
// The recall on an ask is a CEILING, not a decision — gate.Check takes min(ceiling, what the human
// chose). RecallAlways here is therefore not "remember everything forever"; it is "the human may
// choose forever, and the approver offers it". A lower ceiling would leave both approvers showing an
// Always button that silently resolved to a session grant, which is worse than not offering it: the
// person believes they answered a question once and for all, and is asked again tomorrow.
//
// memory is deliberately NOT asked here. A memory write is not an effect in the world; its risk is
// that untrusted text becomes durable context nobody looks at again. In an interactive chat the
// human already sees the call in the transcript as it happens, so an approval prompt buys "before"
// instead of "after" and nothing else. Where nobody is watching it buys the whole thing — see
// agentPolicy.
func policy() gate.Policy {
	return gate.PolicyFunc(func(a gate.Action) gate.Ruling {
		switch a.Kind {
		case tools.NetKind, tools.FileKind:
			return gate.AskWith(gate.RecallAlways)
		default:
			return gate.Allowed()
		}
	})
}

// agentPolicy is the workspace policy plus the one kind an unattended run must not exercise
// silently: memory. A cron agent firing at 6am writes into the store that is folded into EVERY
// future prompt, in every chat, with no human reading its transcript — so it asks out of band, and
// with no device wired the missing approver denies it fail-closed.
//
// The ceiling is the same as the root's, and for the same reason: a human answering "always" for
// "this briefing agent may write briefings/*" is answering a standing question, and the whole point
// of asking out of band is that a person is deciding. Capping it here would ask them again every
// morning while showing a button that said otherwise.
func agentPolicy() gate.Policy {
	base := policy()
	return gate.PolicyFunc(func(a gate.Action) gate.Ruling {
		if a.Kind == memory.Kind {
			return gate.AskWith(gate.RecallAlways)
		}
		return base.Decide(a)
	})
}
