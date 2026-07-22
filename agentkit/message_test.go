package agentkit_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
)

func TestMessage_JSONOmitempty(t *testing.T) {
	t.Run("plain message omits tool fields", func(t *testing.T) {
		b, err := json.Marshal(agentkit.Message{Role: agentkit.RoleUser, Content: "hi"})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		s := string(b)
		for _, key := range []string{"toolCalls", "toolCallID", "durationMs"} {
			if strings.Contains(s, key) {
				t.Fatalf("plain message JSON %s contains %q, want it omitted", s, key)
			}
		}
	})

	t.Run("tool-result message carries id and duration", func(t *testing.T) {
		b, err := json.Marshal(agentkit.Message{
			Role: agentkit.RoleTool, ToolCallID: "c1", Content: "r", DurationMs: 42,
		})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		s := string(b)
		for _, key := range []string{"toolCallID", "durationMs"} {
			if !strings.Contains(s, key) {
				t.Fatalf("tool-result JSON %s missing %q", s, key)
			}
		}
	})
}
