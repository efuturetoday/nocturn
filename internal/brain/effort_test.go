package brain_test

import (
	"context"
	"testing"

	"github.com/efuturetoday/nocturn/internal/brain"
)

func TestParseEffort(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want brain.Effort
	}{
		{"low", brain.EffortLow},
		{"HIGH", brain.EffortHigh}, // case-insensitive
		{" medium ", brain.EffortMedium},
		{"xhigh", brain.EffortXHigh},
		{"minimal", brain.EffortMinimal},
		{"banana", ""}, // unknown → unset
		{"", ""},
	} {
		if got := brain.ParseEffort(tc.in); got != tc.want {
			t.Errorf("ParseEffort(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEffortCtx(t *testing.T) {
	if got := brain.EffortFrom(context.Background()); got != "" {
		t.Fatalf("bare ctx effort = %q, want empty", got)
	}
	ctx := brain.WithEffort(context.Background(), brain.EffortHigh)
	if got := brain.EffortFrom(ctx); got != brain.EffortHigh {
		t.Fatalf("EffortFrom = %q, want high", got)
	}
}
