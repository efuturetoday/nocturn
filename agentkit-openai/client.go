// Package openai adapts an OpenAI-compatible endpoint (via go-openai) to agentkit.LLM:
// streaming SSE, native tool_calls, reasoning deltas — the sole go-openai dependency in the
// tree. It emits answer/reasoning tokens on the ctx event sink.
package openai

import (
	"context"

	goopenai "github.com/sashabaranov/go-openai"

	"github.com/efuturetoday/agentkit"
)

// Client is the go-openai-backed LLM adapter.
type Client struct {
	c         *goopenai.Client
	model     string
	effort    agentkit.Effort
	maxTokens int // provider max_tokens: caps OUTPUT per response (0 = provider default)
	log       agentkit.Logger
}

// Option configures a Client.
type Option func(*Client)

// WithLogger sets the diagnostic logger (default: agentkit.NopLogger()).
func WithLogger(l agentkit.Logger) Option { panic("TODO") }

// WithMaxTokens sets the provider's max_tokens — the per-response OUTPUT cap. This is a
// generation-length limit, distinct from the session's WithTokenLimit (cumulative billed spend).
func WithMaxTokens(n int) Option { panic("TODO") }

// New builds a Client against baseURL with apiKey and a default model.
func New(baseURL, apiKey, model string, opts ...Option) *Client { panic("TODO") }

// Next streams one model step: it accumulates tool_calls per index, synthesizes ids when the
// endpoint omits them, emits Token/Thinking on the ctx sink, fills Step.Tokens from the response
// usage, and returns a final answer or a batch of tool calls. Tool schemas are run through
// sanitizeSchema first.
func (c *Client) Next(ctx context.Context, conv []agentkit.Message, tools []agentkit.ToolSpec) (agentkit.Step, error) {
	panic("TODO")
}

var _ agentkit.LLM = (*Client)(nil)
