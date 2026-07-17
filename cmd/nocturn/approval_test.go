package main

import (
	"context"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/session"
)

// fakeSink is a client that, on being shown an approval, immediately picks a chosen
// option — standing in for the human answering on a session's event stream.
type fakeSink struct {
	intent string
	labels []string
	choose int // the option index to pick
}

func (s *fakeSink) PresentApproval(intent string, labels []string, apply func(choice int)) {
	s.intent, s.labels = intent, labels
	apply(s.choose)
}

// The attended pipe end to end: a turn that carries a session.ApprovalSink on its ctx
// has its approval routed to THAT sink (not the global inline prompt), and the sink's
// chosen option flows back through apply → token → the parked Request, which returns
// the picked outcome. This is the missing consumer that makes the two-pipes model real.
func TestAttendedPipe_RoutesApprovalToCtxSink(t *testing.T) {
	sink := &fakeSink{choose: 1} // pick the second option ("Allow this session")

	var approvals *hitl.Engine
	approvals = hitl.NewEngine([]byte("test-key"), &tuiNotifier{ /* default, unused here */ },
		hitl.WithRouter(func(rctx context.Context) hitl.Notifier {
			if s := session.ApprovalSinkFrom(rctx); s != nil {
				return attendedNotifier{sink: s, resolve: approvals.Resolve}
			}
			return nil
		}))

	choices := []hitl.Choice{
		{Label: "Allow once", Outcome: hitl.Approved},
		{Label: "Allow this session", Outcome: hitl.ApprovedSession},
		{Label: "Deny", Outcome: hitl.Denied},
	}

	ctx := session.WithApprovalSink(context.Background(), sink)
	out, err := approvals.Request(ctx, "Send email to x@a", choices, time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if out != hitl.ApprovedSession {
		t.Fatalf("outcome = %v, want ApprovedSession (the sink picked option 1)", out)
	}
	if sink.intent != "Send email to x@a" {
		t.Fatalf("sink intent = %q", sink.intent)
	}
	if len(sink.labels) != 3 || sink.labels[0] != "Allow once" || sink.labels[2] != "Deny" {
		t.Fatalf("sink labels = %v, want the three choice labels", sink.labels)
	}
}

// With NO approval sink on ctx, the router does not return the attended notifier — an
// unattended/detached path falls through to the engine's other channels (here: the
// default), so the attended pipe never hijacks a run that carries no sink.
func TestAttendedPipe_NoSinkFallsThrough(t *testing.T) {
	routedAttended := false
	var approvals *hitl.Engine
	approvals = hitl.NewEngine([]byte("k"), notifierFunc(func(string, []hitl.Option) error { return nil }),
		hitl.WithRouter(func(rctx context.Context) hitl.Notifier {
			if s := session.ApprovalSinkFrom(rctx); s != nil {
				routedAttended = true
				return attendedNotifier{sink: s, resolve: approvals.Resolve}
			}
			return nil
		}))
	// A bare ctx (no WithApprovalSink) → the router must not pick the attended pipe.
	_, _ = approvals.Request(context.Background(), "x", []hitl.Choice{{Label: "Deny", Outcome: hitl.Denied}}, 10*time.Millisecond)
	if routedAttended {
		t.Fatal("attended notifier was selected for a run with no approval sink on ctx")
	}
}

// notifierFunc adapts a func to hitl.Notifier for the fall-through test.
type notifierFunc func(intent string, options []hitl.Option) error

func (f notifierFunc) Notify(intent string, options []hitl.Option) error { return f(intent, options) }
