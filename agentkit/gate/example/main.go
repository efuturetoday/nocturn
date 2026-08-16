// Command example exercises the gate package without an LLM: a guarded tool asks once, the answer
// is remembered so the second call passes silently, and a denied tool is refused.
package main

import (
	"context"
	"fmt"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
)

type scriptApprover struct {
	approve bool
	recall  gate.Recall
	asks    int
}

func (s *scriptApprover) Ask(_ context.Context, a gate.Action, _ gate.Recall, _ []gate.Grant) (bool, gate.Grant, gate.Recall, error) {
	s.asks++
	fmt.Printf("  [approver] asked about %+v -> approve=%v recall=%v\n", a, s.approve, s.recall)
	return s.approve, gate.Grant{Kind: a.Kind, Target: a.Target}, s.recall, nil
}

func main() {
	send, _ := agentkit.NewTool("send", "send a message",
		func(_ context.Context, args string) (string, error) { return "sent: " + args, nil })
	pay, _ := agentkit.NewTool("pay", "pay an invoice",
		func(_ context.Context, args string) (string, error) { return "paid: " + args, nil })
	exec, _ := agentkit.NewTool("exec", "run a shell command",
		func(context.Context, string) (string, error) { return "ran", nil })

	// send: ask, remember always. pay: ask EVERY time (irreversible). exec: denied.
	policy := gate.PolicyFunc(func(a gate.Action) gate.Ruling {
		switch a.Kind {
		case "exec":
			return gate.Denied()
		case "pay":
			return gate.AskWith(gate.RecallNever)
		case "send":
			return gate.AskWith(gate.RecallAlways)
		default:
			return gate.Allowed()
		}
	})
	approver := &scriptApprover{approve: true, recall: gate.RecallAlways}
	ctx := gate.With(context.Background(), policy, gate.NewMemGrants(), approver)

	guardedSend := gate.Wrap(send)
	out1, err1 := guardedSend.Call(ctx, `{"x":1}`)
	fmt.Printf("send #1: out=%q err=%v  asks=%d\n", out1, err1, approver.asks)
	out2, err2 := guardedSend.Call(ctx, `{"x":2}`)
	fmt.Printf("send #2: out=%q err=%v  asks=%d (RecallAlways: grant covers -> no ask)\n", out2, err2, approver.asks)

	guardedPay := gate.Wrap(pay)
	out3, err3 := guardedPay.Call(ctx, `{"n":1}`)
	fmt.Printf("pay  #1: out=%q err=%v  asks=%d\n", out3, err3, approver.asks)
	out4, err4 := guardedPay.Call(ctx, `{"n":2}`)
	fmt.Printf("pay  #2: out=%q err=%v  asks=%d (RecallNever: asks AGAIN)\n", out4, err4, approver.asks)

	out5, err5 := gate.Wrap(exec).Call(ctx, `{}`)
	fmt.Printf("exec:    out=%q err=%v (policy deny)\n", out5, err5)
}
