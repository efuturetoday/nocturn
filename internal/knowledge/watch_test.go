package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// A daemon that started after a document was added must notice it without waiting an interval, and
// must keep noticing afterwards. Both halves in one test, under a fake clock so nothing sleeps.
func TestWatch_ReconcilesImmediatelyAndThenOnTheTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		emb := &fakeEmbedder{model: "m", dims: 8}
		s, corpus := storeFixture(t, emb)
		write(t, corpus, "first.md", "# First\n\nadded before the daemon started\n")

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go s.Watch(ctx, time.Minute)

		synctest.Wait()
		if got := indexed(t, s); len(got) != 1 {
			t.Fatalf("after starting, indexed %v — want the file that was already there", got)
		}

		// A document dropped in while it runs is picked up on the next tick, with no restart.
		write(t, corpus, "second.md", "# Second\n\nadded while running\n")
		time.Sleep(time.Minute)
		synctest.Wait()
		if got := indexed(t, s); len(got) != 2 {
			t.Fatalf("after a tick, indexed %v — want the new file too", got)
		}

		// And a deletion leaves the index the same way.
		removeFile(t, corpus, "first.md")
		time.Sleep(time.Minute)
		synctest.Wait()
		got := indexed(t, s)
		if len(got) != 1 || got[0] != "second.md" {
			t.Fatalf("after deleting, indexed %v — want only second.md", got)
		}
	})
}

// The usual cause of a failed run is the provider being briefly unreachable. A watcher that stopped
// after one timeout would leave the index stale until somebody restarted the daemon, with nothing
// saying so.
func TestWatch_SurvivesAFailedRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		emb := &fakeEmbedder{model: "m", dims: 8}
		emb.setFailOn("poison")
		s, corpus := storeFixture(t, emb)
		write(t, corpus, "bad.md", "# Bad\n\nthis contains poison\n")

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go s.Watch(ctx, time.Minute)

		synctest.Wait()
		if got := indexed(t, s); len(got) != 0 {
			t.Fatalf("indexed %v despite the provider failing", got)
		}

		// Provider recovers; the next tick indexes what the failed run could not.
		emb.setFailOn("")
		time.Sleep(time.Minute)
		synctest.Wait()
		if got := indexed(t, s); len(got) != 1 {
			t.Fatalf("indexed %v after recovery — the watcher did not survive the failure", got)
		}
	})
}

// indexed is what the store currently holds, failing the test rather than returning an error
// nobody at the call site would do anything with.
func indexed(t *testing.T, s *Store) []string {
	t.Helper()
	p, err := s.Paths()
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	return p
}

func removeFile(t *testing.T, dir, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
}

// slowEmbedder blocks inside Embed and records the deepest concurrency it ever saw, so a test can
// assert that two reconciles never overlap rather than assume it.
type slowEmbedder struct {
	fakeEmbedder
	takes time.Duration

	mu     sync.Mutex
	inside int
	peak   int
	runs   int
}

func (e *slowEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	e.inside++
	e.runs++
	e.peak = max(e.peak, e.inside)
	e.mu.Unlock()

	time.Sleep(e.takes)

	e.mu.Lock()
	e.inside--
	e.mu.Unlock()
	return e.fakeEmbedder.Embed(ctx, texts)
}

func (e *slowEmbedder) seen() (peak, runs int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.peak, e.runs
}

// A reconcile that outlives its interval must not overlap the next one, and the missed ticks must
// not pile up into a queue that then runs back to back.
//
// Two independent reasons it cannot, and the test covers both together: the loop is a single
// goroutine that only waits for a tick AFTER a run returns, and time.Ticker drops ticks nobody is
// waiting for rather than buffering them.
func TestWatch_TicksNeverOverlapOrPileUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		emb := &slowEmbedder{
			fakeEmbedder: fakeEmbedder{model: "m", dims: 8},
			takes:        5 * time.Minute, // far longer than the interval below
		}
		s, corpus := storeFixture(t, emb)
		write(t, corpus, "a.md", "# A\n\nfirst\n")

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go s.Watch(ctx, time.Minute)

		// Long enough for five ticks to have fired during one five-minute run.
		time.Sleep(6 * time.Minute)
		synctest.Wait()

		peak, runs := emb.seen()
		if peak != 1 {
			t.Errorf("%d reconciles were inside the provider at once, want 1", peak)
		}
		// One run for the immediate pass. A second may have started after it finished; what must NOT
		// happen is the five missed ticks each producing one.
		if runs > 2 {
			t.Errorf("%d provider runs after one overlong reconcile, want at most 2 — missed ticks piled up", runs)
		}
	})
}
