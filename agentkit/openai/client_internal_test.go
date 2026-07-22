package openai

// Internal (white-box) tests for client.go's unexported buildMessages — per CLAUDE.md §9,
// unexported behavior is tested in the same-package test file; the public API lives in
// client_test.go (package openai_test).

import (
	"reflect"
	"testing"

	goopenai "github.com/sashabaranov/go-openai"

	"github.com/efuturetoday/nocturn/agentkit"
)

func TestBuildMessages_RoleMapping(t *testing.T) {
	tests := []struct {
		name string
		in   agentkit.Message
		want goopenai.ChatCompletionMessage
	}{
		{
			name: "system",
			in:   agentkit.Message{Role: agentkit.RoleSystem, Content: "you are helpful"},
			want: goopenai.ChatCompletionMessage{Role: goopenai.ChatMessageRoleSystem, Content: "you are helpful"},
		},
		{
			name: "assistant with tool calls",
			in: agentkit.Message{
				Role:      agentkit.RoleAssistant,
				ToolCalls: []agentkit.ToolCall{{ID: "id1", Tool: "get_weather", Args: `{"city":"NYC"}`}},
			},
			want: goopenai.ChatCompletionMessage{
				Role: goopenai.ChatMessageRoleAssistant,
				ToolCalls: []goopenai.ToolCall{{
					ID:       "id1",
					Type:     goopenai.ToolTypeFunction,
					Function: goopenai.FunctionCall{Name: "get_weather", Arguments: `{"city":"NYC"}`},
				}},
			},
		},
		{
			name: "assistant without tool calls",
			in:   agentkit.Message{Role: agentkit.RoleAssistant, Content: "here you go"},
			want: goopenai.ChatCompletionMessage{Role: goopenai.ChatMessageRoleAssistant, Content: "here you go"},
		},
		{
			name: "tool result carries tool_call_id",
			in:   agentkit.Message{Role: agentkit.RoleTool, Content: "72F", ToolCallID: "id1"},
			want: goopenai.ChatCompletionMessage{Role: goopenai.ChatMessageRoleTool, Content: "72F", ToolCallID: "id1"},
		},
		{
			name: "user is the default role",
			in:   agentkit.Message{Role: agentkit.RoleUser, Content: "hi"},
			want: goopenai.ChatCompletionMessage{Role: goopenai.ChatMessageRoleUser, Content: "hi"},
		},
		{
			name: "unknown role falls back to user",
			in:   agentkit.Message{Role: agentkit.Role("mystery"), Content: "hi"},
			want: goopenai.ChatCompletionMessage{Role: goopenai.ChatMessageRoleUser, Content: "hi"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildMessages([]agentkit.Message{tt.in})
			if len(got) != 1 {
				t.Fatalf("buildMessages len = %d, want 1", len(got))
			}
			if !reflect.DeepEqual(got[0], tt.want) {
				t.Errorf("buildMessages(%+v)\n got = %+v\nwant = %+v", tt.in, got[0], tt.want)
			}
		})
	}
}
