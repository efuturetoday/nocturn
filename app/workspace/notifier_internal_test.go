package workspace

import (
	"context"
	"errors"
	"testing"

	"github.com/efuturetoday/nocturn/app/tools"
)

// recordingNotifier stands in for the out-of-band sender (a push, or the terminal).
type recordingNotifier struct {
	got tools.Notification
	n   int
	err error
}

func (r *recordingNotifier) Notify(_ context.Context, n tools.Notification) error {
	r.got = n
	r.n++
	return r.err
}

// When a device is in the foreground the message goes over the live connection (the observer) and the
// out-of-band sender is NOT called — routing is either/or, like an approval, so there is no duplicate.
// The observer sees it already stamped with the workspace, since that is what a device routes on.
func TestNotifier_DeviceActive_RoutesInAppNotPush(t *testing.T) {
	next := &recordingNotifier{}
	n := &notifier{ws: "home", next: next, active: func() bool { return true }}

	var seen []tools.Notification
	n.observe(func(msg tools.Notification) { seen = append(seen, msg) })

	if err := n.Notify(context.Background(), tools.Notification{
		Kind: tools.RemindKind, ChatID: "chat-1", Title: "Timer", Message: "stand up",
	}); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if len(seen) != 1 {
		t.Fatalf("observer saw %d notifications, want 1", len(seen))
	}
	if seen[0].Ws != "home" {
		t.Errorf("observer saw ws %q, want %q — it must be stamped before routing", seen[0].Ws, "home")
	}
	if seen[0].ChatID != "chat-1" || seen[0].Kind != tools.RemindKind {
		t.Errorf("observer saw %+v, want the chat id and kind carried through", seen[0])
	}
	if next.n != 0 {
		t.Errorf("out-of-band sender called %d times, want 0 while a device is active", next.n)
	}
}

// When no device is in the foreground the message wakes one out of band (the sender) and the observer
// is NOT called — the mirror of the active case.
func TestNotifier_NoDeviceActive_RoutesPushNotInApp(t *testing.T) {
	next := &recordingNotifier{}
	n := &notifier{ws: "home", next: next, active: func() bool { return false }}

	var seen int
	n.observe(func(tools.Notification) { seen++ })

	if err := n.Notify(context.Background(), tools.Notification{Kind: tools.RemindKind, Message: "hi"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if seen != 0 {
		t.Errorf("observer saw %d notifications, want 0 with no active device", seen)
	}
	if next.n != 1 {
		t.Fatalf("out-of-band sender called %d times, want 1", next.n)
	}
	if next.got.Ws != "home" {
		t.Errorf("sender saw ws %q, want %q", next.got.Ws, "home")
	}
}

// A nil presence probe means no device is active — the terminal case, where every notification takes
// the sender (print) path.
func TestNotifier_NilProbe_TakesSenderPath(t *testing.T) {
	next := &recordingNotifier{}
	n := &notifier{ws: "home", next: next} // active nil

	var seen int
	n.observe(func(tools.Notification) { seen++ })

	if err := n.Notify(context.Background(), tools.Notification{Message: "hi"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if seen != 0 || next.n != 1 {
		t.Errorf("observer=%d sender=%d, want observer=0 sender=1", seen, next.n)
	}
}

// The out-of-band path reports its error (nothing reached the user); the in-app path does not, since
// an active device already received it. Only the former can mean "no device reached".
func TestNotifier_SenderError_IsReported(t *testing.T) {
	wantErr := errors.New("apns unreachable")
	n := &notifier{ws: "home", next: &recordingNotifier{err: wantErr}, active: func() bool { return false }}

	if err := n.Notify(context.Background(), tools.Notification{Message: "hi"}); !errors.Is(err, wantErr) {
		t.Errorf("Notify = %v, want the sender's error", err)
	}
}

// A device is active but no observer is wired yet (before Serve wires one): rather than drop the
// message, fall through to the sender.
func TestNotifier_ActiveButNoObserver_FallsThroughToSender(t *testing.T) {
	next := &recordingNotifier{}
	n := &notifier{ws: "home", next: next, active: func() bool { return true }}

	if err := n.Notify(context.Background(), tools.Notification{Message: "hi"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if next.n != 1 {
		t.Errorf("sender called %d times, want 1 (no observer to take it)", next.n)
	}
}

// No sender and no active device: nothing to deliver on, but Notify must not fail — a delivery that
// was never configured is not a failure.
func TestNotifier_NoSenderNoActive_Succeeds(t *testing.T) {
	n := &notifier{ws: "home"} // next nil, active nil

	if err := n.Notify(context.Background(), tools.Notification{Message: "hi"}); err != nil {
		t.Fatalf("Notify with nothing wired = %v, want nil", err)
	}
}
