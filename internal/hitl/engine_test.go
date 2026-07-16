package hitl_test

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/efuturetoday/nocturn/internal/deadline"
	"github.com/efuturetoday/nocturn/internal/hitl"
)

// captureNotifier stores the offered options instead of pushing to a phone, so a
// test can play the human by resolving one of their tokens.
type captureNotifier struct {
	options chan []hitl.Option
}

func (n *captureNotifier) Notify(_ string, options []hitl.Option) error {
	n.options <- options
	return nil
}

func newEngine() (*hitl.Engine, *captureNotifier) {
	n := &captureNotifier{options: make(chan []hitl.Option, 1)}
	return hitl.NewEngine([]byte("test-host-key"), n), n
}

// the standard approve/deny choices used across tests
var choices = []hitl.Choice{
	{Label: "Allow", Outcome: hitl.Approved},
	{Label: "Deny", Outcome: hitl.Denied},
}

func tokenFor(opts []hitl.Option, o hitl.Outcome) string {
	for _, opt := range opts {
		if opt.Outcome == o {
			return opt.Token
		}
	}
	return ""
}

func TestEngine_ApproveReleasesCaller(t *testing.T) {
	e, n := newEngine()
	res := make(chan hitl.Outcome, 1)
	go func() {
		out, _ := e.Request(context.Background(), "send email to boss", choices, 2*time.Second)
		res <- out
	}()

	opts := <-n.options
	if err := e.Resolve(tokenFor(opts, hitl.Approved)); err != nil {
		t.Fatalf("resolve approve: %v", err)
	}
	if out := <-res; out != hitl.Approved {
		t.Fatalf("got %v, want Approved", out)
	}
}

func TestEngine_DenyReleasesCaller(t *testing.T) {
	e, n := newEngine()
	res := make(chan hitl.Outcome, 1)
	go func() {
		out, _ := e.Request(context.Background(), "delete all files", choices, 2*time.Second)
		res <- out
	}()

	opts := <-n.options
	if err := e.Resolve(tokenFor(opts, hitl.Denied)); err != nil {
		t.Fatalf("resolve deny: %v", err)
	}
	if out := <-res; out != hitl.Denied {
		t.Fatalf("got %v, want Denied", out)
	}
}

// No decision within the ttl denies — fail closed.
func TestEngine_TimeoutDenies(t *testing.T) {
	e, _ := newEngine()
	out, err := e.Request(context.Background(), "risky action", choices, 30*time.Millisecond)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if out != hitl.Denied {
		t.Fatalf("timeout must deny, got %v", out)
	}
}

// A token is single-use: a second Resolve of the same token is rejected.
func TestEngine_TokenIsSingleUse(t *testing.T) {
	e, n := newEngine()
	res := make(chan hitl.Outcome, 1)
	go func() {
		out, _ := e.Request(context.Background(), "wire payment", choices, 2*time.Second)
		res <- out
	}()

	opts := <-n.options
	tok := tokenFor(opts, hitl.Approved)
	if err := e.Resolve(tok); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	<-res
	if err := e.Resolve(tok); err == nil {
		t.Fatal("second resolve of a consumed token must be rejected")
	}
}

func TestEngine_ResolveRejectsGarbageToken(t *testing.T) {
	e, _ := newEngine()
	if err := e.Resolve("not-a-valid-token"); err == nil {
		t.Fatal("garbage token must be rejected")
	}
}

// While waiting for the human, Request pauses any execution budget on ctx, so the
// wait is bounded by the ttl, not by that budget. Deterministic via testing/synctest:
// inside the bubble time is virtualized, so the real time.AfterFunc in deadline is
// faked and time only advances when every goroutine is durably blocked. Once the
// human is paged (Request pauses the budget before Notify), we let virtual time pass
// FAR past the 40ms budget but well under the huge ttl; the budget is paused so it
// must not fire, and then the human's approval wins.
func TestEngine_PausesBudgetDuringWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e, n := newEngine()
		ctx, cancel := deadline.WithBudget(context.Background(), 40*time.Millisecond)
		defer cancel()

		res := make(chan hitl.Outcome, 1)
		go func() {
			out, _ := e.Request(ctx, "slow approval", choices, time.Hour)
			res <- out
		}()

		opts := <-n.options         // paged; Request has already paused the budget
		time.Sleep(1 * time.Second) // virtual: past the 40ms budget (paused), far under the ttl
		if context.Cause(ctx) == context.DeadlineExceeded {
			t.Fatal("budget expired while paused during the wait")
		}
		if err := e.Resolve(tokenFor(opts, hitl.Approved)); err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if out := <-res; out != hitl.Approved {
			t.Fatalf("got %v, want Approved — the budget must be paused during the wait", out)
		}
	})
}

// routeNotifier records how often it was picked and self-approves so Request returns.
type routeNotifier struct {
	calls   int
	resolve func(string) error
}

func (n *routeNotifier) Notify(_ string, options []hitl.Option) error {
	n.calls++
	return n.resolve(tokenFor(options, hitl.Approved))
}

// A single engine routes each request to a channel chosen from its context — the
// seam that sends an attended run to the console and an unattended one to the phone.
// The engine owns the channel; the router is decoupled (a plain ctx value here, not
// the capability autonomy type). A nil router result falls back to the default.
func TestEngine_RouterPicksChannelFromContext(t *testing.T) {
	type oobKey struct{}
	inter := &routeNotifier{}
	oob := &routeNotifier{}
	e := hitl.NewEngine([]byte("k"), inter, hitl.WithRouter(func(ctx context.Context) hitl.Notifier {
		if ctx.Value(oobKey{}) != nil {
			return oob
		}
		return nil // default → interactive
	}))
	inter.resolve, oob.resolve = e.Resolve, e.Resolve

	// No flag → default (interactive) channel.
	if _, err := e.Request(context.Background(), "x", choices, time.Second); err != nil {
		t.Fatal(err)
	}
	if inter.calls != 1 || oob.calls != 0 {
		t.Fatalf("attended: inter=%d oob=%d, want 1/0", inter.calls, oob.calls)
	}

	// Flag set → out-of-band channel; the same engine resolves it.
	ctx := context.WithValue(context.Background(), oobKey{}, true)
	if _, err := e.Request(ctx, "x", choices, time.Second); err != nil {
		t.Fatal(err)
	}
	if oob.calls != 1 || inter.calls != 1 {
		t.Fatalf("unattended: inter=%d oob=%d, want inter unchanged, oob 1", inter.calls, oob.calls)
	}
}
