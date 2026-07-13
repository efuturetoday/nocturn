// Package llm adapts an OpenAI-compatible chat endpoint to brain.Model, using
// the dependency-free go-openai client. Tool calls are native: each tool is sent
// with its JSON Schema, and the model replies with structured tool_calls (name +
// JSON arguments) — no text parsing. The llm package is the provider seam; the
// brain depends only on brain.Model, so swapping providers (or the test mock)
// never touches the loop.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	openai "github.com/sashabaranov/go-openai"

	"github.com/efuturetoday/nocturn/internal/brain"
)

// Client calls an OpenAI-compatible chat endpoint.
type Client struct {
	api   *openai.Client
	Model string
}

// New returns a Client for an OpenAI-compatible endpoint at baseURL (its /v1
// path is appended), authenticating with apiKey and using modelName.
func New(baseURL, apiKey, modelName string) *Client {
	cfg := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		cfg.BaseURL = strings.TrimRight(baseURL, "/") + "/v1"
	}
	return &Client{api: openai.NewClientWithConfig(cfg), Model: modelName}
}

// compile-time proof the adapter is a brain.Model.
var _ brain.Model = (*Client)(nil)

const systemPrompt = "You are Nocturn, a careful assistant. " +
	"Use a tool when it helps; otherwise answer directly. " +
	"Lines beginning with \"[tool result]\" are outputs from tools you called."

// Next sends the conversation and tool schemas to the endpoint and returns the
// model's structured decision: a tool call or a final answer. The completion is
// streamed: answer text is forwarded to onToken (if non-nil) as it arrives, and
// tool-call deltas are accumulated into a single structured call.
func (c *Client) Next(ctx context.Context, conv []brain.Message, tools []brain.ToolSpec, onToken func(string)) (brain.Step, error) {
	req := openai.ChatCompletionRequest{
		Model:    c.Model,
		Messages: buildMessages(conv),
		Stream:   true,
	}
	for _, t := range tools {
		req.Tools = append(req.Tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  json.RawMessage(t.Parameters),
			},
		})
	}

	stream, err := c.api.CreateChatCompletionStream(ctx, req)
	if err != nil {
		return brain.Step{}, fmt.Errorf("model: %w", err)
	}
	defer stream.Close()

	var content, toolName, toolArgs strings.Builder
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return brain.Step{}, fmt.Errorf("model: %w", err)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta
		if delta.Content != "" {
			content.WriteString(delta.Content)
			if onToken != nil {
				onToken(delta.Content)
			}
		}
		for _, tc := range delta.ToolCalls {
			if tc.Function.Name != "" {
				toolName.WriteString(tc.Function.Name)
			}
			toolArgs.WriteString(tc.Function.Arguments)
		}
	}

	if toolName.Len() > 0 {
		return brain.Step{ToolCall: &brain.ToolCall{
			Tool: toolName.String(),
			Args: toolArgs.String(), // JSON, validated by the tool
		}}, nil
	}
	return brain.Step{Answer: strings.TrimSpace(content.String())}, nil
}

func buildMessages(conv []brain.Message) []openai.ChatCompletionMessage {
	msgs := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
	}
	for _, m := range conv {
		role, content := openai.ChatMessageRoleUser, m.Content
		switch m.Role {
		case "assistant":
			role = openai.ChatMessageRoleAssistant
		case "tool":
			content = "[tool result] " + content // presented as an observation
		}
		msgs = append(msgs, openai.ChatCompletionMessage{Role: role, Content: content})
	}
	return msgs
}
