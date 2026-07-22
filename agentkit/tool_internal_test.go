package agentkit

import (
	"context"
	"testing"
)

// Same-package coverage for the ctx-carried call-id / frame plumbing.

func TestNextCallID_NoCounter_ReturnsZero(t *testing.T) {
	if got := nextCallID(context.Background()); got != 0 {
		t.Fatalf("nextCallID(bg) = %d, want 0 (no counter installed)", got)
	}
}

func TestWithCounter_InheritIfPresent(t *testing.T) {
	ctx1 := withCounter(context.Background())
	c1 := ctx1.Value(counterKey{})
	ctx2 := withCounter(ctx1) // already present → same counter, not a fresh one
	c2 := ctx2.Value(counterKey{})
	if c1 != c2 {
		t.Fatal("withCounter installed a fresh counter, want the inherited one")
	}
	// Ids stay globally unique across the tree: the shared counter increments monotonically.
	if got := nextCallID(ctx1); got != 1 {
		t.Fatalf("first id = %d, want 1", got)
	}
	if got := nextCallID(ctx2); got != 2 {
		t.Fatalf("second id (via inherited ctx) = %d, want 2", got)
	}
}

func TestTruncateChars_RuneSafe(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{"unbounded when zero", "abc", 0, "abc"},
		{"ascii truncated", "héllo", 3, "hél"},
		{"multibyte cut on rune boundary", "日本語ab", 2, "日本"},
		{"limit above length keeps all", "ab", 5, "ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateChars(tt.in, tt.limit); got != tt.want {
				t.Fatalf("truncateChars(%q, %d) = %q, want %q", tt.in, tt.limit, got, tt.want)
			}
		})
	}
}
