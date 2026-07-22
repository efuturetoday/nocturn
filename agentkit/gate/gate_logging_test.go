package gate_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
)

// capHandler records every log record's level, message, and flattened attrs (including With-baked
// ones), so a test can assert on what gate.Check logged.
type capHandler struct {
	mu    *sync.Mutex
	recs  *[]capRec
	attrs []slog.Attr
}

type capRec struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

func newCap() *capHandler { return &capHandler{mu: &sync.Mutex{}, recs: &[]capRec{}} }

func (h *capHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capHandler) Handle(_ context.Context, r slog.Record) error {
	m := map[string]string{}
	for _, a := range h.attrs {
		m[a.Key] = a.Value.String()
	}
	r.Attrs(func(a slog.Attr) bool { m[a.Key] = a.Value.String(); return true })
	h.mu.Lock()
	*h.recs = append(*h.recs, capRec{r.Level, r.Message, m})
	h.mu.Unlock()
	return nil
}

func (h *capHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &capHandler{mu: h.mu, recs: h.recs, attrs: append(append([]slog.Attr{}, h.attrs...), as...)}
}

func (h *capHandler) WithGroup(string) slog.Handler { return h }

// TestCheck_LogsDeny: a policy deny is traced at Warn with the action's kind/target and a reason —
// the human-in-the-loop core must not be a black box.
func TestCheck_LogsDeny(t *testing.T) {
	cap := newCap()
	ctx := gate.With(context.Background(),
		gate.PolicyFunc(func(gate.Action) gate.Ruling { return gate.Denied() }),
		gate.NewMemGrants(), nil)
	ctx = gate.WithLogger(ctx, agentkit.SlogLogger(slog.New(cap)))

	_ = gate.Check(ctx, gate.Action{Kind: "net", Target: "evil.com"}, nil)

	var found bool
	for _, r := range *cap.recs {
		if r.level == slog.LevelWarn && r.msg == "gate deny" {
			found = true
			if r.attrs["kind"] != "net" || r.attrs["target"] != "evil.com" || r.attrs["reason"] != "policy" {
				t.Fatalf("deny log missing fields: %+v", r.attrs)
			}
			if r.attrs["component"] != "gate" {
				t.Errorf("deny log not tagged component=gate: %+v", r.attrs)
			}
		}
	}
	if !found {
		t.Fatalf("no Warn 'gate deny' logged; records=%+v", *cap.recs)
	}
}

// TestCheck_DenyReasons: each deny returns its reason-specific sentinel, and every one still matches
// the ErrDenied umbrella so existing callers are unaffected.
func TestCheck_DenyReasons(t *testing.T) {
	deny := gate.PolicyFunc(func(gate.Action) gate.Ruling { return gate.Denied() })
	ask := gate.PolicyFunc(func(gate.Action) gate.Ruling { return gate.AskWith(gate.RecallSession) })
	act := gate.Action{Kind: "net", Target: "x"}

	// Policy deny.
	err := gate.Check(gate.With(context.Background(), deny, gate.NewMemGrants(), nil), act, nil)
	if !errors.Is(err, gate.ErrDeniedPolicy) || !errors.Is(err, gate.ErrDenied) {
		t.Fatalf("policy deny = %v, want ErrDeniedPolicy (⊂ ErrDenied)", err)
	}
	// Ask with no grant and no approver → unattended.
	err = gate.Check(gate.With(context.Background(), ask, gate.NewMemGrants(), nil), act, nil)
	if !errors.Is(err, gate.ErrDeniedUnattended) || !errors.Is(err, gate.ErrDenied) {
		t.Fatalf("unattended deny = %v, want ErrDeniedUnattended (⊂ ErrDenied)", err)
	}
	// Distinct: a policy deny is NOT an unattended deny.
	if errors.Is(gate.ErrDeniedPolicy, gate.ErrDeniedUnattended) {
		t.Fatal("deny sentinels are not distinguishable")
	}
}

// TestCheck_Silent: with no logger installed, Check must not panic (nop path).
func TestCheck_Silent(t *testing.T) {
	ctx := gate.With(context.Background(),
		gate.PolicyFunc(func(gate.Action) gate.Ruling { return gate.Denied() }), gate.NewMemGrants(), nil)
	if err := gate.Check(ctx, gate.Action{Kind: "net", Target: "x"}, nil); err == nil {
		t.Fatal("want denied")
	}
}
