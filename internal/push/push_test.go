package push_test

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/efuturetoday/nocturn/internal/push"
)

func TestRegistry_RegisterPersistDedupUnregister(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")

	r := push.LoadRegistry(path)
	if len(r.Tokens()) != 0 {
		t.Fatal("fresh registry must be empty")
	}

	if err := r.Register("tok-a", "phone"); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("tok-a", "phone-renamed"); err != nil { // repeat token → no duplicate
		t.Fatal(err)
	}
	if err := r.Register("tok-b", "tablet"); err != nil {
		t.Fatal(err)
	}
	if got := r.Tokens(); len(got) != 2 || !slices.Contains(got, "tok-a") || !slices.Contains(got, "tok-b") {
		t.Fatalf("tokens = %v, want exactly [tok-a tok-b]", got)
	}

	// Persisted: a fresh Load sees the same devices.
	if got := push.LoadRegistry(path).Tokens(); len(got) != 2 {
		t.Fatalf("reloaded tokens = %v, want 2 persisted", got)
	}

	if err := r.Unregister("tok-a"); err != nil {
		t.Fatal(err)
	}
	if got := r.Tokens(); len(got) != 1 || got[0] != "tok-b" {
		t.Fatalf("after unregister: %v, want [tok-b]", got)
	}
	if got := push.LoadRegistry(path).Tokens(); len(got) != 1 {
		t.Fatalf("unregister not persisted: %v", got)
	}
}

// fakeSender records the messages/tokens it was asked to deliver — the double a serve test
// (3b) drives to prove an approval fires a wake push.
type fakeSender struct {
	msgs   []push.Message
	tokens [][]string
}

func (f *fakeSender) Send(_ context.Context, m push.Message, tokens []string) error {
	f.msgs = append(f.msgs, m)
	f.tokens = append(f.tokens, tokens)
	return nil
}

// Sender is a port: a fake satisfies it, so higher layers are testable without a provider.
var _ push.Sender = (*fakeSender)(nil)

func TestFakeSender_Delivers(t *testing.T) {
	f := &fakeSender{}
	_ = f.Send(context.Background(), push.Message{Title: "Approve?", Body: "Send email"}, []string{"tok-a"})
	if len(f.msgs) != 1 || f.msgs[0].Title != "Approve?" || len(f.tokens[0]) != 1 {
		t.Fatalf("sender did not record the delivery: %+v", f)
	}
}
