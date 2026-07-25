package tools_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/secret"
	"github.com/efuturetoday/nocturn/internal/tools"
)

// TestNotify_TargetIsHostOwnedUserChannel proves the notify gate always targets the constant, host-
// owned "user" channel — the model supplies only the text, never the destination.
func TestNotify_TargetIsHostOwnedUserChannel(t *testing.T) {
	var seen []gate.Action
	ctx := capturePolicy(context.Background(), &seen, func(gate.Action) gate.Ruling { return gate.Allowed() })
	notify := toolFrom(t, tools.Config{Notifier: &fakeNotifier{}}, "notify")

	if _, err := notify.Call(ctx, `{"message":"anything","title":"t"}`); err != nil {
		t.Fatalf("notify: %v", err)
	}
	if len(seen) != 1 || seen[0].Kind != tools.NotifyKind || seen[0].Target != "user" {
		t.Fatalf("notify gated on %+v, want notify/user", seen)
	}
}

// TestNotify_Egress_BlocksSmuggledSecret proves a smuggled vault value is blocked before delivery — the
// notifier is never called.
func TestNotify_Egress_BlocksSmuggledSecret(t *testing.T) {
	store := secret.NewStore()
	store.Set("api", []byte("SUPERSECRETVALUE123"))
	sc := secret.NewScanner(store)
	fn := &fakeNotifier{}

	notify := toolFrom(t, tools.Config{Notifier: fn, Scanner: sc}, "notify")
	_, err := notify.Call(allowAll(context.Background()), `{"message":"here: SUPERSECRETVALUE123"}`)
	if err == nil {
		t.Fatal("smuggled secret was not blocked")
	}
	if !strings.Contains(err.Error(), "egress blocked") {
		t.Fatalf("expected an egress block, got %v", err)
	}
	if fn.count() != 0 {
		t.Fatal("notifier delivered a message that leaked a secret")
	}
}

// TestNotify_GateDeny_NotDelivered proves a gate denial refuses delivery and never calls the notifier.
func TestNotify_GateDeny_NotDelivered(t *testing.T) {
	fn := &fakeNotifier{}
	notify := toolFrom(t, tools.Config{Notifier: fn}, "notify")
	if _, err := notify.Call(denyAll(context.Background()), `{"message":"hi"}`); err == nil {
		t.Fatal("denied notify returned no error")
	}
	if fn.count() != 0 {
		t.Fatal("notifier was called despite the gate denial")
	}
}

// TestNotify_MissingMessage_Rejected proves an empty message is refused before the gate.
func TestNotify_MissingMessage_Rejected(t *testing.T) {
	notify := toolFrom(t, tools.Config{Notifier: &fakeNotifier{}}, "notify")
	_, err := notify.Call(allowAll(context.Background()), `{"message":""}`)
	if err == nil || !strings.Contains(err.Error(), "missing required field: message") {
		t.Fatalf("empty message not clearly rejected: %v", err)
	}
}

// TestNotify_NilScanner_Delivers proves notify works with no scanner installed: the message is
// delivered as-is.
func TestNotify_NilScanner_Delivers(t *testing.T) {
	fn := &fakeNotifier{}
	notify := toolFrom(t, tools.Config{Notifier: fn}, "notify") // no Scanner
	if _, err := notify.Call(allowAll(context.Background()), `{"message":"plain","title":"hey"}`); err != nil {
		t.Fatalf("notify: %v", err)
	}
	if last, ok := fn.last(); !ok || last.message != "plain" || last.title != "hey" {
		t.Fatalf("notifier got %+v, want {hey plain}", last)
	}
}

// TestNotify_NotifierError_Wrapped proves a notifier failure surfaces as a wrapped notify error.
func TestNotify_NotifierError_Wrapped(t *testing.T) {
	fn := &fakeNotifier{err: errors.New("push failed")}
	notify := toolFrom(t, tools.Config{Notifier: fn}, "notify")
	_, err := notify.Call(allowAll(context.Background()), `{"message":"hi"}`)
	if err == nil || !strings.Contains(err.Error(), "notify:") || !strings.Contains(err.Error(), "push failed") {
		t.Fatalf("notifier error not wrapped clearly: %v", err)
	}
}
