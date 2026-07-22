package gate

// White-box (package gate): these cases inspect unexported machinery — Ruling's decision/recall fields,
// the load-bearing Recall iota order, and the perms carried in ctx via from.

import (
	"context"
	"testing"
)

// The Ruling constructors set the right decision, and an Ask always carries an explicit Recall. The
// zero value is fail-closed: a zero Recall is RecallNever (ask every time), and a zero Ruling is an Ask.
func TestRuling_Constructors(t *testing.T) {
	if got := Allowed(); got.decision != decisionAllow {
		t.Errorf("Allowed().decision = %v, want decisionAllow", got.decision)
	}
	if got := Denied(); got.decision != decisionDeny {
		t.Errorf("Denied().decision = %v, want decisionDeny", got.decision)
	}
	if got := AskWith(RecallSession); got.decision != decisionAsk || got.recall != RecallSession {
		t.Errorf("AskWith(RecallSession) = %+v, want {decisionAsk, RecallSession}", got)
	}

	// Fail-closed: the zero Recall is the safest (Never), and the zero Ruling defaults to an Ask.
	var zeroRecall Recall
	if zeroRecall != RecallNever {
		t.Errorf("zero Recall = %v, want RecallNever", zeroRecall)
	}
	if got := AskWith(zeroRecall); got.recall != RecallNever {
		t.Errorf("AskWith(zero).recall = %v, want RecallNever", got.recall)
	}
	if (Ruling{}).decision != decisionAsk {
		t.Errorf("zero Ruling.decision = %v, want decisionAsk (fail-closed)", (Ruling{}).decision)
	}
}

// The Recall order is load-bearing: values ascend from most restrictive to least, so Check combines the
// policy ceiling and the human's choice with min() — the more restrictive always wins.
func TestRecall_Ordering_MinRestrictiveWins(t *testing.T) {
	if !(RecallNever < RecallSession && RecallSession < RecallAlways) {
		t.Fatalf("Recall order broken: Never=%d Session=%d Always=%d", RecallNever, RecallSession, RecallAlways)
	}
	tests := []struct {
		name    string
		ceiling Recall
		chosen  Recall
		want    Recall
	}{
		{"never caps always", RecallNever, RecallAlways, RecallNever},
		{"session caps always", RecallSession, RecallAlways, RecallSession},
		{"chosen never under session ceiling", RecallSession, RecallNever, RecallNever},
		{"both always", RecallAlways, RecallAlways, RecallAlways},
		{"symmetric", RecallAlways, RecallSession, RecallSession},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := min(tt.ceiling, tt.chosen); got != tt.want {
				t.Errorf("min(%v, %v) = %v, want %v", tt.ceiling, tt.chosen, got, tt.want)
			}
		})
	}
}

// With installs the machinery in ctx; from reads it back, and it flows unchanged to any derived (nested)
// ctx — a sub-agent inherits the same perms and gains no new authority. No machinery = nil.
func TestWith_InheritedByNestedCtx(t *testing.T) {
	if from(context.Background()) != nil {
		t.Fatal("from on a bare ctx = non-nil, want nil (gating opt-in)")
	}

	pol := PolicyFunc(func(Action) Ruling { return Denied() })
	g := NewMemGrants()
	ctx := With(context.Background(), pol, g, nil)

	p := from(ctx)
	if p == nil {
		t.Fatal("from after With = nil, want installed perms")
	}
	if p.grants != Grants(g) {
		t.Fatal("installed grants not carried through")
	}
	if p.approver != nil {
		t.Fatal("approver = non-nil, want nil (unattended)")
	}

	// A nested/derived ctx inherits the SAME perms pointer — it cannot widen what With installed.
	type otherKey struct{}
	child := context.WithValue(ctx, otherKey{}, 1)
	if from(child) != p {
		t.Fatal("nested ctx did not inherit the identical perms")
	}
}
