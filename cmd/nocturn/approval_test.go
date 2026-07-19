package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/chat"
	"github.com/efuturetoday/nocturn/internal/hitl"
)

// fakeSink is a client that, on being shown an approval, immediately picks a chosen
// option — standing in for the human answering on a session's event stream.
type fakeSink struct {
	intent  string
	labels  []string
	choose  int  // the option index to pick
	clients bool // what HasClients reports
	cleared bool // set when ClearPending is called
}

func (s *fakeSink) PresentApproval(intent string, labels []string, apply func(choice int)) {
	s.intent, s.labels = intent, labels
	apply(s.choose)
}

func (s *fakeSink) HasClients() bool { return s.clients }
func (s *fakeSink) ClearPending()    { s.cleared = true }

// The attended pipe end to end: a turn that carries a chat.ApprovalSink on its ctx
// has its approval routed to THAT sink (not the global inline prompt), and the sink's
// chosen option flows back through apply → token → the parked Request, which returns
// the picked outcome. This is the missing consumer that makes the two-pipes model real.
func TestAttendedPipe_RoutesApprovalToCtxSink(t *testing.T) {
	sink := &fakeSink{choose: 1} // pick the second option ("Allow this session")

	var approvals *hitl.Engine
	approvals = hitl.NewEngine([]byte("test-key"), &tuiNotifier{ /* default, unused here */ },
		hitl.WithRouter(func(rctx context.Context) hitl.Notifier {
			if s := chat.ApprovalSinkFrom(rctx); s != nil {
				return attendedNotifier{sink: s, resolve: approvals.Resolve}
			}
			return nil
		}))

	choices := []hitl.Choice{
		{Label: "Allow once", Outcome: hitl.Approved},
		{Label: "Allow this session", Outcome: hitl.ApprovedSession},
		{Label: "Deny", Outcome: hitl.Denied},
	}

	ctx := chat.WithApprovalSink(context.Background(), sink)
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
			if s := chat.ApprovalSinkFrom(rctx); s != nil {
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

// spyNotifier records whether it was asked to notify (the out-of-band channel in the
// routing tests). It never answers, so an in-band answer must win.
type spyNotifier struct {
	mu     sync.Mutex
	called bool
}

func (s *spyNotifier) Notify(string, []hitl.Option) error {
	s.mu.Lock()
	s.called = true
	s.mu.Unlock()
	return nil
}
func (s *spyNotifier) was() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.called }

// yes/no are constant presence/availability predicates for the routing tests.
func yes() bool { return true }
func no() bool  { return false }

func newRouterEngine(oob hitl.Notifier, oobReady, active func() bool) *hitl.Engine {
	var eng *hitl.Engine
	eng = hitl.NewEngine([]byte("k"), notifierFunc(func(string, []hitl.Option) error { return nil }),
		hitl.WithRouter(func(rctx context.Context) hitl.Notifier {
			return routeApproval(rctx, oob, oobReady, active, eng.Resolve)
		}))
	return eng
}

var routeChoices = []hitl.Choice{
	{Label: "Allow once", Outcome: hitl.Approved},
	{Label: "Deny", Outcome: hitl.Denied},
}

// No app foreground-active (and a device can receive a push) → the approval is recorded in-band
// (so a reconnecting app can answer it) AND pushed out-of-band to the phone; on resolution the
// in-band prompt clears (no phantom pending). A background chat's Ask reaches the phone.
func TestRouteApproval_NoActive_RecordsInbandAndTeesToOob(t *testing.T) {
	sink := &fakeSink{choose: 0} // answers "Allow once" in-band
	oob := &spyNotifier{}
	eng := newRouterEngine(oob, yes, no) // a device is registered; no app active

	ctx := chat.WithApprovalSink(context.Background(), sink)
	out, err := eng.Request(ctx, "Send email", routeChoices, time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if out != hitl.Approved {
		t.Fatalf("outcome = %v, want Approved (in-band answered)", out)
	}
	if !oob.was() {
		t.Error("no app active → the request must ALSO go out-of-band to the phone")
	}
	if sink.intent != "Send email" {
		t.Error("in-band sink was not presented the request — a pending approval was not recorded")
	}
	if !sink.cleared {
		t.Error("pending not cleared on resolution — a reconnecting client would see a phantom prompt")
	}
}

// An app IS foreground-active → in-band only; the phone must not buzz (the WS reaches it).
func TestRouteApproval_Active_InbandOnly(t *testing.T) {
	sink := &fakeSink{choose: 0}
	oob := &spyNotifier{}
	eng := newRouterEngine(oob, yes, yes) // a device is registered, but an app is active

	ctx := chat.WithApprovalSink(context.Background(), sink)
	if _, err := eng.Request(ctx, "x", routeChoices, time.Second); err != nil {
		t.Fatalf("request: %v", err)
	}
	if oob.was() {
		t.Error("an app is active → the request must NOT also go out-of-band")
	}
	if !sink.cleared {
		t.Error("pending not cleared on resolution")
	}
}

// No OOB-capable device (no push token registered) → no tee even with no app active; the
// approval stays in-band, waiting for a client rather than pushing to nobody.
func TestRouteApproval_NoOOBDevice_InbandOnly(t *testing.T) {
	sink := &fakeSink{choose: 0}
	oob := &spyNotifier{}
	eng := newRouterEngine(oob, no, no) // no registered device, no app active

	ctx := chat.WithApprovalSink(context.Background(), sink)
	if _, err := eng.Request(ctx, "x", routeChoices, time.Second); err != nil {
		t.Fatalf("request: %v", err)
	}
	if oob.was() {
		t.Error("no OOB-capable device → must not push")
	}
}

// No sink at all (a direct background run, no runner) → straight out-of-band when a device is
// available, else nil (the front-end fallback).
func TestRouteApproval_NoSink(t *testing.T) {
	oob := &spyNotifier{}
	if got := routeApproval(context.Background(), oob, yes, no, func(string) error { return nil }); got != oob {
		t.Errorf("no-sink route = %v, want the oob notifier", got)
	}
	if got := routeApproval(context.Background(), oob, no, no, func(string) error { return nil }); got != nil {
		t.Errorf("no-sink but no device → %v, want nil", got)
	}
	if got := routeApproval(context.Background(), nil, no, no, nil); got != nil {
		t.Errorf("no sink and no oob → %v, want nil (front-end fallback)", got)
	}
}
