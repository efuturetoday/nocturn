package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/tools"
)

// wakeInSeconds pulls the clamped delay from a wake result.
func wakeInSeconds(t *testing.T, out string) float64 {
	t.Helper()
	var res struct {
		WakeInSeconds float64 `json:"wakeInSeconds"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("wake result not JSON: %v (%q)", err, out)
	}
	return res.WakeInSeconds
}

// TestWake_ResumesSameChatByID proves a fired wake resolves the SAME chat by id and re-invokes it with
// the note as its prompt: the fake Sessions is asked to Open("c1") and the resumed session's LLM sees
// the note.
func TestWake_ResumesSameChatByID(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		llm := newRecordLLM()
		sess := agentkit.NewSession(context.Background(), llm)
		fs := &fakeSessions{session: sess}
		w := tools.NewWaker()
		w.Bind(fs)

		tool, err := w.Tool()
		if err != nil {
			t.Fatalf("Tool: %v", err)
		}
		ctx := tools.WithChatID(context.Background(), "c1")
		if _, err := tool.Call(ctx, `{"seconds":5,"note":"re-check the deploy"}`); err != nil {
			t.Fatalf("wake: %v", err)
		}

		time.Sleep(6 * time.Second)
		synctest.Wait()

		if ids := fs.openedIDs(); len(ids) != 1 || ids[0] != "c1" {
			t.Fatalf("wake resolved chats %v, want [c1]", ids)
		}
		select {
		case note := <-llm.seen:
			if note != "re-check the deploy" {
				t.Fatalf("resumed session saw note %q, want the wake note", note)
			}
		default:
			t.Fatal("the resumed session never ran a turn with the note")
		}

		sess.Close()
		synctest.Wait()
	})
}

// TestWake_NoChatID_Unavailable proves wake is unavailable without a chat to resume: a bare context
// yields a clear error and schedules nothing.
func TestWake_NoChatID_Unavailable(t *testing.T) {
	w := tools.NewWaker()
	w.Bind(&fakeSessions{})
	tool, err := w.Tool()
	if err != nil {
		t.Fatalf("Tool: %v", err)
	}
	_, err = tool.Call(context.Background(), `{"seconds":5,"note":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "no conversation to resume") {
		t.Fatalf("bare-context wake not clearly refused: %v", err)
	}
	if w.Pending() != 0 {
		t.Fatalf("a refused wake still scheduled: pending=%d", w.Pending())
	}
}

// TestWake_MissingNote_Rejected proves an empty note is refused.
func TestWake_MissingNote_Rejected(t *testing.T) {
	w := tools.NewWaker()
	w.Bind(&fakeSessions{})
	tool, _ := w.Tool()
	ctx := tools.WithChatID(context.Background(), "c1")
	_, err := tool.Call(ctx, `{"seconds":5,"note":""}`)
	if err == nil || !strings.Contains(err.Error(), "missing required field: note") {
		t.Fatalf("empty note not clearly rejected: %v", err)
	}
}

// TestWake_PendingCap_Enforced proves the runaway guard: past MaxPending, a further wake is refused
// with ErrTooManyPending.
func TestWake_PendingCap_Enforced(t *testing.T) {
	w := tools.NewWaker() // NewWaker initializes the internal timer map/logger; set the cap after.
	w.MaxPending = 2
	w.Bind(&fakeSessions{})
	defer w.Cancel()
	tool, _ := w.Tool()
	ctx := tools.WithChatID(context.Background(), "c1")

	for i := 0; i < 2; i++ {
		if _, err := tool.Call(ctx, `{"seconds":3600,"note":"n"}`); err != nil {
			t.Fatalf("wake %d unexpectedly refused: %v", i, err)
		}
	}
	_, err := tool.Call(ctx, `{"seconds":3600,"note":"n"}`)
	if !errors.Is(err, tools.ErrTooManyPending) {
		t.Fatalf("wake past the cap = %v, want ErrTooManyPending", err)
	}
	if w.Pending() != 2 {
		t.Fatalf("pending = %d, want 2 (the refused wake was not scheduled)", w.Pending())
	}
}

// TestWake_DelayClamped proves the delay is clamped into [min, max] — a too-short request rises to the
// minimum, a too-long one is capped at the maximum.
func TestWake_DelayClamped(t *testing.T) {
	w := tools.NewWaker() // defaults: min 1s, max 1h
	w.Bind(&fakeSessions{})
	defer w.Cancel()
	tool, _ := w.Tool()
	ctx := tools.WithChatID(context.Background(), "c1")

	out, err := tool.Call(ctx, `{"seconds":0.001,"note":"n"}`)
	if err != nil {
		t.Fatalf("wake: %v", err)
	}
	if got := wakeInSeconds(t, out); got != 1 {
		t.Fatalf("tiny delay clamped to %v, want 1s (the minimum)", got)
	}

	out, err = tool.Call(ctx, `{"seconds":100000,"note":"n"}`)
	if err != nil {
		t.Fatalf("wake: %v", err)
	}
	if got := wakeInSeconds(t, out); got != 3600 {
		t.Fatalf("huge delay clamped to %v, want 3600s (the maximum)", got)
	}
}

// TestWake_NilSeam_SafeNoOp proves an unbound Waker fires without panicking (the seam is nil, so the
// firing is a safe no-op).
func TestWake_NilSeam_SafeNoOp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := tools.NewWaker() // never Bound
		tool, _ := w.Tool()
		ctx := tools.WithChatID(context.Background(), "c1")
		if _, err := tool.Call(ctx, `{"seconds":5,"note":"n"}`); err != nil {
			t.Fatalf("wake: %v", err)
		}
		time.Sleep(6 * time.Second)
		synctest.Wait()
		if w.Pending() != 0 {
			t.Fatalf("timer not drained after firing: pending=%d", w.Pending())
		}
	})
}

// TestWake_UnresolvableChat_NoOp proves a fire that resolves to a nil session (a chat that cannot be
// re-opened) is a safe no-op.
func TestWake_UnresolvableChat_NoOp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fs := &fakeSessions{session: nil} // Open returns nil
		w := tools.NewWaker()
		w.Bind(fs)
		tool, _ := w.Tool()
		ctx := tools.WithChatID(context.Background(), "c1")
		if _, err := tool.Call(ctx, `{"seconds":5,"note":"n"}`); err != nil {
			t.Fatalf("wake: %v", err)
		}
		time.Sleep(6 * time.Second)
		synctest.Wait()
		if ids := fs.openedIDs(); len(ids) != 1 || ids[0] != "c1" {
			t.Fatalf("expected one Open(c1) attempt, got %v", ids)
		}
	})
}

// TestWake_Cancel_DisarmsButKeeps proves Cancel is shutdown, not deletion: after cancelling,
// advancing past the delays resolves no chat, but the wakes are still outstanding — they are what the
// next Restore re-arms. Deleting them here is the behaviour that used to lose a continuation across a
// restart.
func TestWake_Cancel_DisarmsButKeeps(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fs := &fakeSessions{session: nil}
		w := tools.NewWaker()
		w.Bind(fs)
		tool, _ := w.Tool()
		ctx := tools.WithChatID(context.Background(), "c1")
		for range 3 {
			if _, err := tool.Call(ctx, `{"seconds":60,"note":"n"}`); err != nil {
				t.Fatalf("wake: %v", err)
			}
		}
		if w.Pending() != 3 {
			t.Fatalf("pending = %d, want 3", w.Pending())
		}
		w.Cancel()
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if len(fs.openedIDs()) != 0 {
			t.Fatalf("a cancelled wake still resolved a chat: %v", fs.openedIDs())
		}
		if w.Pending() != 3 {
			t.Fatalf("pending after Cancel = %d, want 3 (cancel disarms, it does not delete)", w.Pending())
		}
	})
}

// TestWake_SurvivesReopen proves the whole point of persisting: a wake set before the workspace goes
// away is still there when it comes back, and it fires. Before this, a wake lived only in a
// time.AfterFunc — a restart dropped it with no log line and no error.
func TestWake_SurvivesReopen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "wakes.json")

		// First life: schedule, then shut down the way Workspace.Close does.
		first := tools.NewWaker(tools.WithWakeStore(path))
		first.Bind(&fakeSessions{})
		tool, _ := first.Tool()
		ctx := tools.WithChatID(context.Background(), "c1")
		if _, err := tool.Call(ctx, `{"seconds":600,"note":"re-check the deploy"}`); err != nil {
			t.Fatalf("wake: %v", err)
		}
		first.Cancel()

		// Second life: a fresh Waker over the same store, bound and restored.
		llm := newRecordLLM()
		sess := agentkit.NewSession(context.Background(), llm)
		fs := &fakeSessions{session: sess}
		second := tools.NewWaker(tools.WithWakeStore(path))
		if second.Pending() != 1 {
			t.Fatalf("pending after reopen = %d, want 1", second.Pending())
		}
		second.Bind(fs)
		second.Restore()

		time.Sleep(11 * time.Minute)
		synctest.Wait()
		if got := fs.openedIDs(); len(got) != 1 || got[0] != "c1" {
			t.Fatalf("restored wake resolved %v, want [c1]", got)
		}
		if second.Pending() != 0 {
			t.Fatalf("pending after firing = %d, want 0", second.Pending())
		}
		select {
		case note := <-llm.seen:
			if note != "re-check the deploy" {
				t.Fatalf("restored session saw note %q, want the wake note", note)
			}
		default:
			t.Fatal("the restored session never ran a turn with the note")
		}

		sess.Close()
		synctest.Wait()
	})
}

// TestWake_Restore_OverdueFiresPromptly proves the catch-up: a wake that came due while the process
// was down fires as soon as it is restored, rather than waiting out a delay that already elapsed.
func TestWake_Restore_OverdueFiresPromptly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "wakes.json")
		overdue := []tools.Wake{{
			ID: "wake-1", FireAt: time.Now().Add(-time.Hour), ChatID: "c9", Note: "n",
		}}
		data, err := json.Marshal(overdue)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}

		sess := agentkit.NewSession(context.Background(), newRecordLLM())
		fs := &fakeSessions{session: sess}
		w := tools.NewWaker(tools.WithWakeStore(path))
		w.Bind(fs)
		w.Restore()
		synctest.Wait()

		if got := fs.openedIDs(); len(got) != 1 || got[0] != "c9" {
			t.Fatalf("overdue wake resolved %v, want [c9]", got)
		}

		sess.Close()
		synctest.Wait()
	})
}

// TestWake_Pending_Count proves Pending reports the number of scheduled-but-unfired wakes.
func TestWake_Pending_Count(t *testing.T) {
	w := tools.NewWaker()
	w.Bind(&fakeSessions{})
	defer w.Cancel()
	tool, _ := w.Tool()
	ctx := tools.WithChatID(context.Background(), "c1")
	if _, err := tool.Call(ctx, `{"seconds":3600,"note":"a"}`); err != nil {
		t.Fatalf("wake: %v", err)
	}
	if _, err := tool.Call(ctx, `{"seconds":3600,"note":"b"}`); err != nil {
		t.Fatalf("wake: %v", err)
	}
	if w.Pending() != 2 {
		t.Fatalf("Pending = %d, want 2", w.Pending())
	}
}

// TestWake_WithChatID_RoundTrip proves the chat-id carrier round-trips through the ctx accessor, and a
// bare context carries none.
func TestWake_WithChatID_RoundTrip(t *testing.T) {
	if got := tools.ChatID(tools.WithChatID(context.Background(), "abc")); got != "abc" {
		t.Fatalf("ChatID round-trip = %q, want abc", got)
	}
	if got := tools.ChatID(context.Background()); got != "" {
		t.Fatalf("bare context ChatID = %q, want empty", got)
	}
}
