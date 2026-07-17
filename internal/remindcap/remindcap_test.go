package remindcap_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/remindcap"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// fakePusher records deliveries. It is mutex-guarded because a reminder fires on its
// own timer goroutine.
type fakePusher struct {
	mu             sync.Mutex
	calls          int
	title, message string
}

func (p *fakePusher) Push(_ context.Context, title, message string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.title, p.message = title, message
	return nil
}
func (p *fakePusher) snapshot() (int, string, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, p.title, p.message
}

func allowGuard() *gateway.Guard {
	return &gateway.Guard{Policy: capability.Policy{Rules: []capability.Rule{
		{Family: capability.Wildcard, TargetGlob: capability.Wildcard, Writes: capability.MatchRead, Effect: capability.Allow, Epoch: capability.Permanent},
	}}}
}

func invoke(r *remindcap.Reminders, name, args string) (string, error) {
	for _, t := range r.Tools() {
		if t.Name == name {
			return t.Invoke(context.Background(), args)
		}
	}
	panic("no tool " + name)
}

// A reminder is enrolled on create and, in fake time, fires precisely at its time —
// delivering the notification and clearing the store (one-shot). synctest advances the
// bubble's clock so time.AfterFunc fires deterministically, no real waiting.
func TestRemind_CreateFiresAtTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := remindcap.LoadStore("")
		push := &fakePusher{}
		r := remindcap.New(allowGuard(), store, push, nil)

		out, err := invoke(r, "remind", `{"when":"in 1h","title":"Hi","message":"dentist"}`)
		if err != nil {
			t.Fatalf("remind: %v", err)
		}
		var res struct{ ID string }
		if json.Unmarshal([]byte(out), &res); res.ID == "" {
			t.Fatalf("create output %q", out)
		}
		if r.Pending() != 1 || len(store.List()) != 1 {
			t.Fatalf("after create: pending=%d store=%d, want 1/1", r.Pending(), len(store.List()))
		}
		if calls, _, _ := push.snapshot(); calls != 0 {
			t.Fatal("fired before its time")
		}

		time.Sleep(time.Hour) // fake time: advances instantly, fires the AfterFunc
		synctest.Wait()       // let the fire goroutine finish

		if calls, title, msg := push.snapshot(); calls != 1 || title != "Hi" || msg != "dentist" {
			t.Fatalf("push calls=%d title=%q message=%q", calls, title, msg)
		}
		if r.Pending() != 0 || len(store.List()) != 0 {
			t.Fatalf("after fire: pending=%d store=%d, want 0/0", r.Pending(), len(store.List()))
		}
	})
}

// A secret in the reminder message is blocked at CREATE — nothing is stored/enrolled.
func TestRemind_LeakScanBlocksAtCreate(t *testing.T) {
	st := secret.NewStore()
	st.Set("tok", []byte("supersecretvalue1234"))
	store := remindcap.LoadStore("")
	r := remindcap.New(allowGuard(), store, &fakePusher{}, secret.NewScanner(st))

	if _, err := invoke(r, "remind", `{"when":"in 1h","message":"pw is supersecretvalue1234"}`); !errors.Is(err, secret.ErrLeaked) {
		t.Fatalf("err = %v, want ErrLeaked", err)
	}
	if r.Pending() != 0 || len(store.List()) != 0 {
		t.Fatal("a leaking reminder was stored/enrolled")
	}
}

func TestRemind_Cancel(t *testing.T) {
	store := remindcap.LoadStore("")
	r := remindcap.New(allowGuard(), store, &fakePusher{}, nil)

	out, _ := invoke(r, "remind", `{"when":"in 1h","message":"x"}`)
	var res struct{ ID string }
	json.Unmarshal([]byte(out), &res)

	cancelOut, err := invoke(r, "remind.cancel", `{"id":"`+res.ID+`"}`)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if !strings.Contains(cancelOut, `"cancelled":true`) {
		t.Fatalf("cancel out = %q", cancelOut)
	}
	if r.Pending() != 0 || len(store.List()) != 0 {
		t.Fatal("reminder survived cancel")
	}
}

// Reminders persist across a reload, and Restore re-enrolls them.
func TestRemind_PersistAndRestore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reminders.json")
	store := remindcap.LoadStore(path)
	r := remindcap.New(allowGuard(), store, &fakePusher{}, nil)
	if _, err := invoke(r, "remind", `{"when":"in 2h","message":"persisted"}`); err != nil {
		t.Fatalf("remind: %v", err)
	}

	reloaded := remindcap.LoadStore(path)
	if len(reloaded.List()) != 1 || reloaded.List()[0].Message != "persisted" {
		t.Fatalf("reloaded store = %v, want the persisted reminder", reloaded.List())
	}
	r2 := remindcap.New(allowGuard(), reloaded, &fakePusher{}, nil)
	r2.Restore()
	if r2.Pending() != 1 {
		t.Fatalf("Restore enrolled %d, want 1", r2.Pending())
	}
}

func TestRemind_BadWhen(t *testing.T) {
	r := remindcap.New(allowGuard(), remindcap.LoadStore(""), &fakePusher{}, nil)
	for _, args := range []string{`{"when":"whenever","message":"x"}`, `{"when":"in -1h","message":"x"}`, `{"message":"x"}`} {
		if _, err := invoke(r, "remind", args); err == nil {
			t.Errorf("args %s: want error", args)
		}
	}
}
