package gate

// decision is a policy's allow / ask / deny outcome. It is internal — surfaced only through a Ruling
// (built with Allowed / Denied / AskWith), never named by a consumer.
type decision int

const (
	decisionAsk decision = iota
	decisionAllow
	decisionDeny
)

// Recall caps, per Kind, how long an approval MAY be remembered — the policy's ceiling on the human's
// choice. It bounds irreversible actions: a pay/delete Kind can require RecallNever so it is asked
// every single time, no matter what the human picks. (Prefixed because a bare "Always" would collide
// with Scope.Always.)
//
// The zero value is RecallNever — the SAFEST option — so a forgotten Recall fails closed: it asks
// every time rather than silently remembering. Loosening to RecallSession/RecallAlways is explicit.
//
// The order is LOAD-BEARING: values ascend from most restrictive (Never) to least (Always), so
// Check combines the policy ceiling and the human's choice with min() — the more restrictive wins.
// Do not reorder.
type Recall int

const (
	RecallNever   Recall = iota // 0 = never remembered; the grant cache is skipped and it asks every time
	RecallSession               // remembered only this session
	RecallAlways                // remembered durably (the human's Always is honored)
)

// Ruling is a policy's verdict on an action. Its fields are unexported: build it with Allowed, Denied
// or AskWith so an Ask ALWAYS states its Recall — there is no way to construct an ask that silently
// defaults its remembering.
type Ruling struct {
	decision decision
	recall   Recall
}

// Allowed runs the action freely — no ask, nothing remembered.
func Allowed() Ruling { return Ruling{decision: decisionAllow} }

// Denied refuses the action.
func Denied() Ruling { return Ruling{decision: decisionDeny} }

// AskWith requires human approval, with recall capping how long an approval may be remembered
// (RecallNever = ask every time, RecallSession = this session, RecallAlways = durable).
func AskWith(recall Recall) Ruling { return Ruling{decision: decisionAsk, recall: recall} }

// Policy decides, from the action alone, whether it is freely allowed, must be asked, or denied — and
// for an Ask, the Recall ceiling on remembering it. It is the static, coarse layer; the grant +
// approval layer sits on top of an Ask.
type Policy interface {
	Decide(a Action) Ruling
}

// PolicyFunc adapts a plain function to Policy. Use it for per-Kind control, e.g. return
// AskWith(RecallNever) for an irreversible Kind so it is asked every time.
type PolicyFunc func(Action) Ruling

func (f PolicyFunc) Decide(a Action) Ruling { return f(a) }
