package hitl_test

import (
	"context"
	"testing"
	"time"

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
