package notifycap_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
	"github.com/efuturetoday/nocturn/internal/notifycap"
	"github.com/efuturetoday/nocturn/internal/secret"
)

type fakePusher struct {
	calls          int
	title, message string
	err            error
}

func (f *fakePusher) Push(_ context.Context, title, message string) error {
	f.calls++
	f.title, f.message = title, message
	return f.err
}

// allowGuard runs reads silently (base policy) with no approval engine — so if
// notify ever tried to Ask, it would panic on the nil engine. That it doesn't is
// the proof that notify is silent (Write:false → Allow).
func allowGuard() *gateway.Guard {
	return &gateway.Guard{Policy: capability.Policy{Rules: []capability.Rule{
		{Family: capability.Wildcard, TargetGlob: capability.Wildcard, Writes: capability.MatchRead, Effect: capability.Allow, Epoch: capability.Permanent},
	}}}
}

// invoke runs the notify tool with the given JSON args.
func invoke(n *notifycap.Notifier, args string) (string, error) {
	return n.Tools()[0].Invoke(context.Background(), args)
}

func TestNotify_DeliversSilently(t *testing.T) {
	p := &fakePusher{}
	n := notifycap.New(allowGuard(), p, nil)

	out, err := invoke(n, `{"title":"Hi","message":"task done"}`)
	if err != nil {
		t.Fatalf("notify: %v", err)
	}
	if !strings.Contains(out, `"sent":true`) {
		t.Fatalf("out = %q, want sent:true", out)
	}
	if p.calls != 1 || p.title != "Hi" || p.message != "task done" {
		t.Fatalf("pusher got calls=%d title=%q message=%q", p.calls, p.title, p.message)
	}
}

// A secret in the message is blocked on egress — the notification never leaves,
// so notify cannot be used to exfiltrate a vault value to the (public) channel.
func TestNotify_LeakScanBlocks(t *testing.T) {
	store := secret.NewStore()
	store.Set("tok", []byte("abc/def+ghijklmnop")) // a stored vault value
	sc := secret.NewScanner(store)

	p := &fakePusher{}
	n := notifycap.New(allowGuard(), p, sc)

	if _, err := invoke(n, `{"message":"here it is: abc/def+ghijklmnop"}`); !errors.Is(err, secret.ErrLeaked) {
		t.Fatalf("err = %v, want ErrLeaked", err)
	}
	if p.calls != 0 {
		t.Fatal("a leaking notification was delivered — egress scan did not block it")
	}
	// A clean message still goes through.
	if _, err := invoke(n, `{"message":"nothing secret here"}`); err != nil {
		t.Fatalf("clean notify: %v", err)
	}
	if p.calls != 1 {
		t.Fatalf("clean notify not delivered (calls=%d)", p.calls)
	}
}

// notify passes the Guard, so a policy that denies it denies the effect — the
// message is never pushed.
func TestNotify_GuardDenies(t *testing.T) {
	p := &fakePusher{}
	n := notifycap.New(&gateway.Guard{Policy: capability.Policy{}}, p, nil) // deny-by-default

	if _, err := invoke(n, `{"message":"x"}`); !errors.Is(err, gateway.ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
	if p.calls != 0 {
		t.Fatal("a denied notification was delivered")
	}
}

func TestNotify_MissingMessage(t *testing.T) {
	n := notifycap.New(allowGuard(), &fakePusher{}, nil)
	if _, err := invoke(n, `{"title":"only title"}`); err == nil {
		t.Fatal("missing message must error")
	}
	if _, err := invoke(n, `not json`); err == nil {
		t.Fatal("malformed args must error")
	}
}
