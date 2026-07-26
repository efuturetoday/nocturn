package serve

import (
	"context"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/tools"
)

func TestReminderDispatch_BadJSON_PerCommand(t *testing.T) {
	for _, cmd := range []string{"reminder.list", "reminder.cancel"} {
		t.Run(cmd, func(t *testing.T) {
			c := testConn()
			c.reminder(context.Background(), cmd, []byte(`{not json`))
			recvError(t, c, "bad "+cmd)
		})
	}
}

func TestReminder_UnknownAction_Error(t *testing.T) {
	c := testConn()
	c.reminder(context.Background(), "reminder.bogus", []byte(`{"cmd":"reminder.bogus"}`))
	recvError(t, c, "unknown action")
}

func TestReminder_UnknownWorkspace_Errors(t *testing.T) {
	for _, cmd := range []string{"reminder.list", "reminder.cancel"} {
		t.Run(cmd, func(t *testing.T) {
			c := testConn() // empty spaces
			c.reminder(context.Background(), cmd, []byte(`{"cmd":"`+cmd+`","ws":"ghost","id":"x"}`))
			recvError(t, c, "unknown workspace")
		})
	}
}

// A fresh workspace has nothing pending. Listing must answer with an empty ARRAY (never null, which a
// client would have to special-case) rather than fail.
func TestReminderList_Empty_EmptyArray(t *testing.T) {
	c := connWith(openWorkspace(t))
	c.reminder(context.Background(), "reminder.list", []byte(`{"cmd":"reminder.list","ws":"main"}`))

	res, ok := recv(t, c).(ReminderListResult)
	if !ok {
		t.Fatal("expected a ReminderListResult")
	}
	if res.Type != "reminder.list" || res.Ws != "main" {
		t.Errorf("result = {%q %q}, want {\"reminder.list\" \"main\"}", res.Type, res.Ws)
	}
	if res.Reminders == nil {
		t.Error("Reminders is nil — it must marshal as [] so clients need no null case")
	}
	if len(res.Reminders) != 0 {
		t.Errorf("Reminders = %+v, wanted none", res.Reminders)
	}
}

// Cancelling an id that matches nothing is a silent no-op: no reply, no error. The broadcast
// (reminder.changed) is what tells devices anything happened, and nothing did.
func TestReminderCancel_UnknownID_SilentNoOp(t *testing.T) {
	c := connWith(openWorkspace(t))
	c.reminder(context.Background(), "reminder.cancel", []byte(`{"cmd":"reminder.cancel","ws":"main","id":"nope"}`))

	// The handler pushes synchronously to the buffered out channel, so anything it meant to send is
	// already there — no wait needed to prove it sent nothing.
	if n := len(c.control); n != 0 {
		t.Fatalf("expected no reply, got %d message(s): %#v", n, <-c.control)
	}
}

// reminderInfos is the wire projection: RFC3339 with an offset (so a device in another timezone
// renders the daemon's intended instant), an omitted empty title, and pending order preserved.
func TestReminderInfos_WireProjection(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("no tzdata for Europe/Berlin: %v", err)
	}
	at := time.Date(2026, 7, 22, 14, 30, 0, 0, berlin)

	got := reminderInfos([]tools.Reminder{
		{ID: "rem-1", FireAt: at, Message: "stand up", Title: "Timer"},
		{ID: "rem-2", FireAt: at.Add(time.Hour), Message: "no title"},
	})

	if len(got) != 2 {
		t.Fatalf("got %d infos, want 2", len(got))
	}
	if want := "2026-07-22T14:30:00+02:00"; got[0].FireAt != want {
		t.Errorf("FireAt = %q, want %q (offset carries the zone)", got[0].FireAt, want)
	}
	if got[0].ID != "rem-1" || got[0].Message != "stand up" || got[0].Title != "Timer" {
		t.Errorf("info[0] = %+v, want the reminder's fields verbatim", got[0])
	}
	if got[1].Title != "" {
		t.Errorf("info[1].Title = %q, want empty (omitempty drops it)", got[1].Title)
	}
}

// An empty set projects to an empty ARRAY, not nil — the same client contract as the list result.
func TestReminderInfos_Empty_NotNil(t *testing.T) {
	if got := reminderInfos(nil); got == nil || len(got) != 0 {
		t.Errorf("reminderInfos(nil) = %#v, want an empty non-nil slice", got)
	}
}

// Reminders are offered even when no out-of-band sender is configured — a fire still reaches an awake
// device over its live connection — so the accessors work on a workspace opened without a Notifier
// rather than being absent.
func TestWorkspace_ReminderAccessors_WithoutNotifier(t *testing.T) {
	ws := openWorkspace(t) // Host{} carries no Notifier
	if got := ws.Reminders(); got == nil || len(got) != 0 {
		t.Errorf("Reminders() = %#v, want an empty non-nil slice", got)
	}
	if ws.CancelReminder("rem-does-not-exist") {
		t.Error("CancelReminder on an unknown id = true, want false")
	}
	ws.OnReminderChange(func() { t.Error("change callback fired without a reminder changing") })
	ws.OnNotification(func(tools.Notification) { t.Error("notification observer fired with no notification") })
}
