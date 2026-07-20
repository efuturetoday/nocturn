// Command example exercises the gate package without an LLM: a guarded tool asks once, the answer
// is remembered so the second call passes silently, and a denied tool is refused.
package main

import (
	"context"
	"fmt"

	"github.com/efuturetoday/agentkit"
	"github.com/efuturetoday/agentkit-gate"
)

type scriptApprover struct {
	decision gate.Decision
	scope    gate.Scope
	asks     int
}

func (s *scriptApprover) Ask(_ context.Context, a gate.Action, _ []gate.Grant) (gate.Decision, gate.Grant, gate.Scope, error) {
	s.asks++
	fmt.Printf("  [approver] asked about %+v -> %v/%v\n", a, s.decision, s.scope)
	return s.decision, gate.Grant{Kind: a.Kind, Target: a.Target}, s.scope, nil
}

func main() {
	send, _ := agentkit.NewTool("send", "send a message",
		func(_ context.Context, args string) (string, error) { return "sent: " + args, nil })
	exec, _ := agentkit.NewTool("exec", "run a shell command",
		func(context.Context, string) (string, error) { return "ran", nil })

	// send is guarded (ask), exec is denied.
	policy := gate.Classify([]string{"send"}, []string{"exec"})
	approver := &scriptApprover{decision: gate.Allow, scope: gate.Always}
	ctx := gate.With(context.Background(), policy, gate.NewMemGrants(), approver)

	guardedSend := gate.Wrap(send)
	out1, err1 := guardedSend.Call(ctx, `{"x":1}`)
	fmt.Printf("call1: out=%q err=%v  asks=%d\n", out1, err1, approver.asks)

	out2, err2 := guardedSend.Call(ctx, `{"x":2}`)
	fmt.Printf("call2: out=%q err=%v  asks=%d (grant covers -> no ask)\n", out2, err2, approver.asks)

	out3, err3 := gate.Wrap(exec).Call(ctx, `{}`)
	fmt.Printf("exec:  out=%q err=%v (policy deny)\n", out3, err3)
}
