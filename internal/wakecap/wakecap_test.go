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

func invoke(w *wakecap.Waker, args string) (string, error) {
	return w.Tool().Invoke(context.Background(), args)
}

// A wake resumes (in fake time) after its delay, exactly once, then clears.
func TestWake_ResumesAfterDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var notes []string
		w := wakecap.New(func(note string) { mu.Lock(); notes = append(notes, note); mu.Unlock() })

		if _, err := invoke(w, `{"seconds":300,"note":"re-check the deploy"}`); err != nil {
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
	w := wakecap.New(func(string) {})
	defer w.Cancel() // stop the real timers these create

	read := func(args string) float64 {
		out, err := invoke(w, args)
		if err != nil {
			t.Fatalf("wake %s: %v", args, err)
		}
		var r struct {
			WakeInSeconds float64 `json:"wakeInSeconds"`
		}
		json.Unmarshal([]byte(out), &r)
		return r.WakeInSeconds
	}
	if got := read(`{"seconds":5,"note":"x"}`); got != 60 {
		t.Errorf("5s → %v, want clamped up to 60", got)
	}
	if got := read(`{"seconds":99999,"note":"x"}`); got != 3600 {
		t.Errorf("99999s → %v, want clamped down to 3600", got)
	}
}

// The pending cap bounds runaway self-waking.
func TestWake_MaxPending(t *testing.T) {
	w := wakecap.New(func(string) {})
	w.MaxPending = 2
	defer w.Cancel()

	mustOK := func(note string) {
		if _, err := invoke(w, `{"seconds":300,"note":"`+note+`"}`); err != nil {
			t.Fatalf("wake %s: %v", note, err)
		}
	}
	mustOK("a")
	mustOK("b")
	if _, err := invoke(w, `{"seconds":300,"note":"c"}`); !errors.Is(err, wakecap.ErrTooManyPending) {
		t.Fatalf("3rd wake err=%v, want ErrTooManyPending", err)
	}
}

// Cancel drops pending wakes so they never resume.
func TestWake_Cancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		fired := 0
		w := wakecap.New(func(string) { mu.Lock(); fired++; mu.Unlock() })

		invoke(w, `{"seconds":300,"note":"x"}`)
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

func TestWake_MissingNote(t *testing.T) {
	w := wakecap.New(func(string) {})
	defer w.Cancel()
	if _, err := invoke(w, `{"seconds":300}`); err == nil {
		t.Fatal("missing note must error")
	}
	if _, err := invoke(w, `not json`); err == nil {
		t.Fatal("malformed args must error")
	}
}
