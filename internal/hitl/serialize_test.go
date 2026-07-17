package hitl_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/hitl"
)

type notifierFunc func(intent string, options []hitl.Option) error

func (f notifierFunc) Notify(intent string, options []hitl.Option) error { return f(intent, options) }

// With a serialized notifier, two concurrent approval Requests never present
// their prompts at the same time: the second Notify does not start until the
// first has been released.
// NOTE: deliberately NOT migrated to testing/synctest. Serialize uses a sync.Mutex
// (serialize.go), and mutex contention is NOT durably blocking in a synctest bubble
// (go.dev/blog/testing-time — "Mutex acquisition is not durably blocking"). The second
// request parks on that mutex, so synctest.Wait() could never observe it settle and the
// test would hang. This is a real-time concurrency test; the 50ms window is a pragmatic
// "give the second goroutine time to reach the mutex" guard, which is the right tool here.
func TestSerialize_OneApprovalAtATime(t *testing.T) {
	entered := make(chan string, 2)
	release := make(chan struct{})
	base := notifierFunc(func(intent string, _ []hitl.Option) error {
		entered <- intent // signal we are inside Notify (a prompt is on screen)
		<-release         // block as if waiting for the human
		return nil
	})
	eng := hitl.NewEngine([]byte("test-host-key"), hitl.Serialize(base))

	var wg sync.WaitGroup
	wg.Add(2)
	start := func(intent string) {
		go func() {
			defer wg.Done()
			_, _ = eng.Request(context.Background(), intent, choices, 200*time.Millisecond)
		}()
	}
	start("A")
	start("B")

	<-entered // exactly one prompt gets in
	select {
	case x := <-entered:
		t.Fatalf("a second prompt (%q) appeared before the first was released — not serialized", x)
	case <-time.After(50 * time.Millisecond):
	}

	close(release) // let the first finish → the second may now proceed
	<-entered      // the second prompt now appears
	wg.Wait()
}

// Serialize is idempotent and nil-safe.
func TestSerialize_IdempotentAndNilSafe(t *testing.T) {
	if hitl.Serialize(nil) != nil {
		t.Fatal("Serialize(nil) should be nil")
	}
	base := notifierFunc(func(string, []hitl.Option) error { return nil })
	once := hitl.Serialize(base)
	if hitl.Serialize(once) != once {
		t.Fatal("Serialize should be idempotent (already-serialized returned unchanged)")
	}
}
