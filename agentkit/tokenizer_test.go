package agentkit_test

import (
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
)

func TestApproxTokenizer_Count(t *testing.T) {
	tk := agentkit.ApproxTokenizer()
	tests := []struct {
		name string
		text string
		want int // (runeCount + 3) / 4
	}{
		{"empty is zero", "", 0},
		{"four ascii runes", "abcd", 1},        // (4+3)/4 = 1
		{"seven ascii runes", "abcdefg", 2},    // (7+3)/4 = 2
		{"multibyte counts runes", "日本語ab", 2}, // 5 runes → (5+3)/4 = 2, not by bytes
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tk.Count(tt.text)
			if err != nil {
				t.Fatalf("Count(%q) err = %v, want nil (never errors)", tt.text, err)
			}
			if got != tt.want {
				t.Fatalf("Count(%q) = %d, want %d", tt.text, got, tt.want)
			}
		})
	}
}
