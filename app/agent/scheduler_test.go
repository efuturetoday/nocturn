package agent_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/efuturetoday/nocturn/app/agent"
)

// TestScheduler_Start_AlignsToMinute_StopsOnCtxCancel drives the tick loop under a synthetic clock.
// It proves the loop fires on minute boundaries (each firing lands exactly on :00) and that
// cancelling the context stops Start (its goroutine returns).
func TestScheduler_Start_AlignsToMinute_StopsOnCtxCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var fires []time.Time
		fire := func(_ context.Context, _ agent.Agent) {
			mu.Lock()
			fires = append(fires, time.Now())
			mu.Unlock()
		}

		agents := agent.Set{"ticker": {Name: "ticker", When: "* * * * *"}}
		s := agent.NewScheduler(agents, slog.New(slog.NewTextHandler(io.Discard, nil)), fire)

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			s.Start(ctx)
			close(done)
		}()

		// Let the synthetic clock advance across three minute boundaries. The bubble's clock starts
		// at 00:00:00, so firings land at 00:01, 00:02, 00:03.
		time.Sleep(3*time.Minute + time.Second)
		synctest.Wait()

		mu.Lock()
		got := append([]time.Time(nil), fires...)
		mu.Unlock()

		if len(got) != 3 {
			t.Fatalf("fired %d times, want 3: %v", len(got), got)
		}
		for i, f := range got {
			if f.Second() != 0 || f.Nanosecond() != 0 {
				t.Errorf("fire %d at %v is not aligned to a minute boundary", i, f.Format(time.RFC3339Nano))
			}
			if i > 0 {
				if d := f.Sub(got[i-1]); d != time.Minute {
					t.Errorf("gap between fire %d and %d = %v, want 1m", i-1, i, d)
				}
			}
		}

		// Cancelling must stop the loop.
		cancel()
		select {
		case <-done:
		case <-time.After(time.Minute):
			t.Fatal("Start did not return after ctx cancel")
		}
	})
}
