package agentkit

import "testing"

// TokenCount.add is unexported, so this lives same-package (the plan filed it under message.go).
func TestTokenCount_Add(t *testing.T) {
	var total TokenCount
	total.add(TokenCount{Prompt: 10, Completion: 5, Total: 15})
	total.add(TokenCount{Prompt: 10, Completion: 5, Total: 15})
	if want := (TokenCount{Prompt: 20, Completion: 10, Total: 30}); total != want {
		t.Fatalf("accumulated = %+v, want %+v", total, want)
	}
}
