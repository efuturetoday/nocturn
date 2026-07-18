package appserver_test

import (
	"encoding/json"
	"testing"

	"github.com/efuturetoday/nocturn/internal/appserver"
	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/session"
)

// A saved conversation with a tool turn must round-trip its tool calls into the snapshot:
// the assistant's call carries the tool name + args + the result matched by tool_call_id,
// so a reconnecting client renders the tool forest, not just the final text.
func TestEncodeSnapshot_ReconstructsToolForest(t *testing.T) {
	snap := session.Snapshot{
		Running: false,
		Messages: []brain.Message{
			{Role: "user", Content: "list my files"},
			// The assistant turn that only called a tool — no text of its own.
			{Role: "assistant", ToolCalls: []brain.ToolCall{{ID: "c1", Tool: "file.list", Args: `{"path":"."}`}}},
			// The tool result, tied to the call by ToolCallID.
			{Role: "tool", ToolCallID: "c1", Content: "inbox.txt\nnotes/"},
			{Role: "assistant", Content: "You have inbox.txt and notes/."},
		},
	}

	b, err := appserver.EncodeSnapshot(snap)
	if err != nil {
		t.Fatalf("EncodeSnapshot: %v", err)
	}

	var got struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
			Tools   []struct {
				Tool   string `json:"tool"`
				Args   string `json:"args"`
				Result string `json:"result"`
			} `json:"tools"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}

	// The empty tool-only assistant turn is kept (it carries the forest); the plumbing
	// role=tool message is not a bubble. So: user, tool-call turn, final answer = 3.
	if len(got.Messages) != 3 {
		t.Fatalf("messages = %d (%s), want 3", len(got.Messages), b)
	}

	forest := got.Messages[1]
	if forest.Role != "assistant" || forest.Content != "" {
		t.Fatalf("message[1] = %+v, want an empty-content assistant turn", forest)
	}
	if len(forest.Tools) != 1 {
		t.Fatalf("message[1].tools = %d, want 1", len(forest.Tools))
	}
	tc := forest.Tools[0]
	if tc.Tool != "file.list" || tc.Args != `{"path":"."}` || tc.Result != "inbox.txt\nnotes/" {
		t.Fatalf("tool frame = %+v, want file.list with its args and matched result", tc)
	}

	if got.Messages[2].Content != "You have inbox.txt and notes/." {
		t.Fatalf("final message = %q, want the answer text", got.Messages[2].Content)
	}
}
