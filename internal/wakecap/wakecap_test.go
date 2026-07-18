package wakecap_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/efuturetoday/nocturn/internal/wakecap"
)

// noop is a resume that does nothing — for tests that only exercise scheduling/clamping.
func noop(string) {}

// invoke calls the wake tool with resume carried on the ctx (as a runner's decorator would).
func invoke(w *wakecap.Waker, resume wakecap.Resume, args string) (string, error) {
	ctx := context.Background()
	if resume != nil {
		ctx = wakecap.WithResume(ctx, resume)
	}
	return w.Tool().Invoke(ctx, args)
}

// A wake resumes (in fake time) after its delay, exactly once, then clears.
func TestWake_ResumesAfterDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var notes []string
		w := wakecap.New()
		resume := func(note string) { mu.Lock(); notes = append(notes, note); mu.Unlock() }

		if _, err := invoke(w, resume, `{"seconds":300,"note":"re-check the deploy"}`); err != nil {
			t.Fatalf("wake: %v", err)
		}
		if w.Pending() != 1 {
			t.Fatalf("pending=%d, want 1", w.Pending())
		}

		time.Sleep(6 * time.Minute) // fake time past the 300s delay
		synctest.Wait()

		mu.Lock()
		got := append([]string(nil), notes...)
		mu.Unlock()
		if len(got) != 1 || got[0] != "re-check the deploy" {
			t.Fatalf("resumed with %v, want one [re-check the deploy]", got)
		}
		if w.Pending() != 0 {
			t.Fatalf("pending after fire=%d, want 0", w.Pending())
		}
	})
}

// The delay is clamped into [60s, 3600s], reflected in wakeInSeconds.
func TestWake_ClampsDelay(t *testing.T) {
	w := wakecap.New()
	defer w.Cancel() // stop the real timers these create

	read := func(args string) float64 {
		out, err := invoke(w, noop, args)
		if err != nil {
			t.Fatalf("wake %s: %v", args, err)
		}
		var r struct {
			WakeInSeconds float64 `json:"wakeInSeconds"`
		}
		json.Unmarshal([]byte(out), &r)
		return r.WakeInSeconds
	}
	if got := read(`{"seconds":0,"note":"x"}`); got != 1 {
		t.Errorf("0s → %v, want clamped up to 1", got)
	}
	if got := read(`{"seconds":2,"note":"x"}`); got != 2 {
		t.Errorf("2s → %v, want 2 (within range, not clamped)", got)
	}
	if got := read(`{"seconds":99999,"note":"x"}`); got != 3600 {
		t.Errorf("99999s → %v, want clamped down to 3600", got)
	}
}

// The pending cap bounds runaway self-waking.
func TestWake_MaxPending(t *testing.T) {
	w := wakecap.New()
	w.MaxPending = 2
	defer w.Cancel()

	mustOK := func(note string) {
		if _, err := invoke(w, noop, `{"seconds":300,"note":"`+note+`"}`); err != nil {
			t.Fatalf("wake %s: %v", note, err)
		}
	}
	mustOK("a")
	mustOK("b")
	if _, err := invoke(w, noop, `{"seconds":300,"note":"c"}`); !errors.Is(err, wakecap.ErrTooManyPending) {
		t.Fatalf("3rd wake err=%v, want ErrTooManyPending", err)
	}
}

// Cancel drops pending wakes so they never resume.
func TestWake_Cancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		fired := 0
		w := wakecap.New()
		resume := func(string) { mu.Lock(); fired++; mu.Unlock() }

		invoke(w, resume, `{"seconds":300,"note":"x"}`)
		w.Cancel()
		if w.Pending() != 0 {
			t.Fatalf("pending after Cancel=%d, want 0", w.Pending())
		}

		time.Sleep(10 * time.Minute)
		synctest.Wait()
		mu.Lock()
		f := fired
		mu.Unlock()
		if f != 0 {
			t.Fatalf("a cancelled wake fired (%d times)", f)
		}
	})
}

// With no resume on the ctx (a bare context — nothing to resume), wake refuses instead of
// scheduling a timer that would fire into the void.
func TestWake_NoResumeOnCtx_Errors(t *testing.T) {
	w := wakecap.New()
	defer w.Cancel()
	if _, err := invoke(w, nil, `{"seconds":300,"note":"x"}`); err == nil {
		t.Fatal("wake with no resume on ctx must error")
	}
	if w.Pending() != 0 {
		t.Fatalf("pending=%d, want 0 (nothing scheduled)", w.Pending())
	}
}

func TestWake_MissingNote(t *testing.T) {
	w := wakecap.New()
	defer w.Cancel()
	if _, err := invoke(w, noop, `{"seconds":300}`); err == nil {
		t.Fatal("missing note must error")
	}
	if _, err := invoke(w, noop, `not json`); err == nil {
		t.Fatal("malformed args must error")
	}
}
