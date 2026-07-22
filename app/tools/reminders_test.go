package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/app/secret"
	"github.com/efuturetoday/nocturn/app/tools"
)

// reminderTool returns the named reminder tool (remind / remind_list / remind_cancel).
func reminderTool(t *testing.T, r *tools.Reminders, name string) agentkit.Tool {
	t.Helper()
	ts, err := r.Tools()
	if err != nil {
		t.Fatalf("Reminders.Tools: %v", err)
	}
	for _, tl := range ts {
		if tl.Spec().Name == name {
			return tl
		}
	}
	t.Fatalf("reminder tool %q not found", name)
	return nil
}

// createdID pulls the "id" from a remind create result.
func createdID(t *testing.T, out string) string {
	t.Helper()
	var res struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("create result not JSON: %v (%q)", err, out)
	}
	if res.ID == "" {
		t.Fatalf("create returned no id: %q", out)
	}
	return res.ID
}

// TestRemind_Egress_BlockedAtCreate proves a smuggled secret is blocked at creation — the reminder is
// never scheduled.
func TestRemind_Egress_BlockedAtCreate(t *testing.T) {
	store := secret.NewStore()
	store.Set("api", []byte("SUPERSECRETVALUE123"))
	sc := secret.NewScanner(store)
	fn := &fakeNotifier{}
	r := tools.NewReminders("", fn, sc)
	defer r.Cancel()

	create := reminderTool(t, r, "remind")
	_, err := create.Call(allowAll(context.Background()), `{"when":"in 1h","message":"leak SUPERSECRETVALUE123"}`)
	if err == nil {
		t.Fatal("smuggled secret at create was not blocked")
	}
	if !strings.Contains(err.Error(), "egress blocked") {
		t.Fatalf("expected an egress block, got %v", err)
	}
	if out, _ := reminderTool(t, r, "remind_list").Call(context.Background(), `{}`); !strings.Contains(out, "[]") {
		t.Fatalf("a blocked reminder was still scheduled: %q", out)
	}
}

// TestRemind_GatedOnRemindKind proves creation passes the RemindKind gate: a denial refuses it.
func TestRemind_GatedOnRemindKind(t *testing.T) {
	var seen []gate.Action
	ctx := capturePolicy(context.Background(), &seen, func(gate.Action) gate.Ruling { return gate.Denied() })
	r := tools.NewReminders("", &fakeNotifier{}, nil)
	defer r.Cancel()

	if _, err := reminderTool(t, r, "remind").Call(ctx, `{"when":"in 1h","message":"hi"}`); err == nil {
		t.Fatal("denied create returned no error")
	}
	if len(seen) != 1 || seen[0].Kind != tools.RemindKind || seen[0].Target != "user" {
		t.Fatalf("remind gated on %+v, want remind/user", seen)
	}
}

// TestRemind_Fire_DeliversNotification proves an armed reminder fires at its time and delivers.
func TestRemind_Fire_DeliversNotification(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fn := &fakeNotifier{}
		r := tools.NewReminders("", fn, nil)
		defer r.Cancel()

		if _, err := reminderTool(t, r, "remind").Call(allowAll(context.Background()), `{"when":"in 1h","message":"ping","title":"T"}`); err != nil {
			t.Fatalf("create: %v", err)
		}
		time.Sleep(time.Hour)
		synctest.Wait()

		if fn.count() != 1 {
			t.Fatalf("reminder did not fire: notifier called %d times", fn.count())
		}
		if last, _ := fn.last(); last.message != "ping" || last.title != "T" {
			t.Fatalf("fired reminder delivered %+v, want {T ping}", last)
		}
	})
}

// TestRemind_Fire_RescansAndDropsLeak proves the fire-time re-scan: a secret stored AFTER creation is
// caught at fire and the reminder is dropped silently rather than delivered.
func TestRemind_Fire_RescansAndDropsLeak(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := secret.NewStore()
		sc := secret.NewScanner(store)
		fn := &fakeNotifier{}
		r := tools.NewReminders("", fn, sc)
		defer r.Cancel()

		// The store is empty at create time, so the message passes the egress scan and is scheduled.
		if _, err := reminderTool(t, r, "remind").Call(allowAll(context.Background()), `{"when":"in 1h","message":"token=SUPERSECRETVALUE123"}`); err != nil {
			t.Fatalf("create: %v", err)
		}
		// The value becomes a known secret between creation and fire.
		store.Set("api", []byte("SUPERSECRETVALUE123"))

		time.Sleep(time.Hour)
		synctest.Wait()

		if fn.count() != 0 {
			t.Fatal("a reminder that became a leak was delivered — fire-time re-scan failed")
		}
	})
}

// TestRemind_Cancel_StopsTimer proves remind_cancel stops the timer: after cancelling, advancing past
// the fire time delivers nothing.
func TestRemind_Cancel_StopsTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fn := &fakeNotifier{}
		r := tools.NewReminders("", fn, nil)
		defer r.Cancel()

		out, err := reminderTool(t, r, "remind").Call(allowAll(context.Background()), `{"when":"in 1h","message":"ping"}`)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		id := createdID(t, out)

		if _, err := reminderTool(t, r, "remind_cancel").Call(context.Background(), `{"id":`+jsonQuote(id)+`}`); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		time.Sleep(time.Hour)
		synctest.Wait()

		if fn.count() != 0 {
			t.Fatal("a cancelled reminder still fired")
		}
	})
}

// TestRemind_Restore_OverdueFiresPromptly proves Restore enrolls persisted reminders and an overdue one
// fires promptly (the delay is clamped to ≥0).
func TestRemind_Restore_OverdueFiresPromptly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "rem.json")
		// Persist one reminder whose FireAt is already in the past (relative to the bubble clock).
		overdue := []tools.Reminder{{
			ID:      "rem-restore-1",
			FireAt:  time.Now().Add(-time.Hour),
			Message: "wake up",
		}}
		data, err := json.Marshal(overdue)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}

		fn := &fakeNotifier{}
		r := tools.NewReminders(path, fn, nil)
		defer r.Cancel()
		r.Restore()
		synctest.Wait()

		if fn.count() != 1 {
			t.Fatalf("overdue reminder did not fire promptly: notifier called %d times", fn.count())
		}
	})
}

// TestRemind_ParseWhen covers the "when" parsing: "in <duration>" and RFC3339 are accepted, while a
// non-positive duration or garbage is a clear error (rejected before the gate).
func TestRemind_ParseWhen(t *testing.T) {
	r := tools.NewReminders("", &fakeNotifier{}, nil)
	defer r.Cancel()
	create := reminderTool(t, r, "remind")
	future := time.Now().Add(2 * time.Hour).Format(time.RFC3339)

	cases := []struct {
		name, when string
		wantErr    bool
	}{
		{"in-duration", "in 30m", false},
		{"rfc3339", future, false},
		{"negative", "in -5m", true},
		{"zero", "in 0s", true},
		{"garbage", "whenever", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := create.Call(context.Background(), `{"when":`+jsonQuote(tc.when)+`,"message":"m"}`)
			if tc.wantErr != (err != nil) {
				t.Fatalf("when=%q err=%v, wantErr=%v", tc.when, err, tc.wantErr)
			}
		})
	}
}

// TestRemind_Persistence_RoundTrip proves a created reminder is persisted 0600 and reloaded by a fresh
// Reminders over the same file.
func TestRemind_Persistence_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rem.json")

	r1 := tools.NewReminders(path, &fakeNotifier{}, nil)
	if _, err := reminderTool(t, r1, "remind").Call(context.Background(), `{"when":"in 2h","message":"remember"}`); err != nil {
		t.Fatalf("create: %v", err)
	}
	r1.Cancel()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("store file not written: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("store perms = %o, want 0600", perm)
	}

	r2 := tools.NewReminders(path, &fakeNotifier{}, nil)
	defer r2.Cancel()
	out, err := reminderTool(t, r2, "remind_list").Call(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "remember") {
		t.Fatalf("reloaded store missing the reminder: %q", out)
	}
}

// TestRemind_List_SortedSoonestFirst proves remind_list returns reminders soonest-first.
func TestRemind_List_SortedSoonestFirst(t *testing.T) {
	r := tools.NewReminders("", &fakeNotifier{}, nil)
	defer r.Cancel()
	create := reminderTool(t, r, "remind")
	if _, err := create.Call(context.Background(), `{"when":"in 2h","message":"later"}`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := create.Call(context.Background(), `{"when":"in 1h","message":"sooner"}`); err != nil {
		t.Fatalf("create: %v", err)
	}
	out, err := reminderTool(t, r, "remind_list").Call(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Index(out, "sooner") > strings.Index(out, "later") {
		t.Fatalf("reminders not sorted soonest-first: %q", out)
	}
}

// TestRemind_Load_MalformedFile_EmptyStore proves a malformed store file loads tolerantly as an empty
// store rather than crashing.
func TestRemind_Load_MalformedFile_EmptyStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rem.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := tools.NewReminders(path, &fakeNotifier{}, nil)
	defer r.Cancel()
	out, err := reminderTool(t, r, "remind_list").Call(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "[]") {
		t.Fatalf("malformed file did not yield an empty store: %q", out)
	}
}
