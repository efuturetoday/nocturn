package knowledge

import (
	"context"
	"os"
	"path/filepath"
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
