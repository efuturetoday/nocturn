// Package openai adapts an OpenAI-compatible endpoint (via go-openai) to agentkit.LLM: streaming
// SSE, native tool_calls, reasoning deltas — the sole go-openai dependency in the tree. It emits
// answer/reasoning tokens on the ctx event sink and fills Step.Tokens from the response usage.
package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	goopenai "github.com/sashabaranov/go-openai"

	"github.com/efuturetoday/agentkit"
)

// Client is the go-openai-backed LLM adapter. It is read-only after New, so it is safe to share
// across concurrent turns.
type Client struct {
	api       *goopenai.Client
	model     string
	effort    agentkit.Effort
	maxTokens int // provider max_tokens: caps OUTPUT per response (0 = provider default)
	log       agentkit.Logger
}

// Option configures a Client.
type Option func(*Client)

// WithLogger sets the diagnostic logger (default: agentkit.NopLogger()). A nil logger is ignored.
func WithLogger(l agentkit.Logger) Option {
	return func(c *Client) {
		if l != nil {
			c.log = l
		}
	}
}

// WithEffort sets the default reasoning effort. A per-turn effort carried on ctx
// (agentkit.EffortFrom, set by the session) overrides it.
func WithEffort(e agentkit.Effort) Option {
	return func(c *Client) { c.effort = e }
}

// WithMaxTokens sets the provider's max_tokens — the per-response OUTPUT cap. This is a
// generation-length limit, distinct from the session's WithTokenLimit (cumulative billed spend).
func WithMaxTokens(n int) Option {
	return func(c *Client) { c.maxTokens = n }
}

// New builds a Client against baseURL (its /v1 path is appended) with apiKey and a default model.
func New(baseURL, apiKey, model string, opts ...Option) *Client {
	cfg := goopenai.DefaultConfig(apiKey)
	if baseURL != "" {
		cfg.BaseURL = strings.TrimRight(baseURL, "/") + "/v1"
	}
	c := &Client{api: goopenai.NewClientWithConfig(cfg), model: model, log: agentkit.NopLogger()}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Next streams one model step: it accumulates tool_calls per index, synthesizes ids when the
// endpoint omits them, emits Token/Thinking on the ctx sink, fills Step.Tokens from the response
// usage, and returns a final answer or a batch of tool calls. Tool schemas are run through
// sanitizeSchema first.
func (c *Client) Next(ctx context.Context, conv []agentkit.Message, tools []agentkit.ToolSpec) (agentkit.Step, error) {
	req := goopenai.ChatCompletionRequest{
		Model:         c.model,
		Messages:      buildMessages(conv),
		Stream:        true,
		StreamOptions: &goopenai.StreamOptions{IncludeUsage: true},
	}
	effort := agentkit.EffortFrom(ctx)
	if effort == "" {
		effort = c.effort
	}
	req.ReasoningEffort = string(effort)
	if c.maxTokens > 0 {
		req.MaxTokens = c.maxTokens
	}
	for _, t := range tools {
		params, changed := sanitizeSchema(t.Parameters)
		if changed {
			c.log.WithContext(ctx).Debug("openai: sanitized tool schema", "tool", t.Name)
		}
		req.Tools = append(req.Tools, goopenai.Tool{
			Type: goopenai.ToolTypeFunction,
			Function: &goopenai.FunctionDefinition{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}

	stream, err := c.api.CreateChatCompletionStream(ctx, req)
	if err != nil {
		return agentkit.Step{}, fmt.Errorf("openai: create stream: %w", err)
	}
	defer stream.Close()

	var content strings.Builder
	acc := newToolAcc()
	var usage agentkit.TokenCount
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return agentkit.Step{}, fmt.Errorf("openai: stream recv: %w", err)
		}
		if chunk.Usage != nil {
			usage = agentkit.TokenCount{
				Prompt:     chunk.Usage.PromptTokens,
				Completion: chunk.Usage.CompletionTokens,
				Total:      chunk.Usage.TotalTokens,
			}
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta
		if delta.Content != "" {
			content.WriteString(delta.Content)
			agentkit.Emit(ctx, agentkit.Token{Text: delta.Content})
		}
		// Reasoning ("extended thinking") streams in its own field — surface it as Thinking, not
		// accumulated into the answer.
		if delta.ReasoningContent != "" {
			agentkit.Emit(ctx, agentkit.Thinking{Text: delta.ReasoningContent})
		}
		// Tool calls stream in fragments keyed by index: name once, arguments in pieces, several
		// calls interleaved. Accumulate PER INDEX, or parallel calls fuse into one garbage call.
		for _, tc := range delta.ToolCalls {
			acc.add(tc)
		}
	}

	step := agentkit.Step{Tokens: usage}
	if calls := acc.calls(); len(calls) > 0 {
		step.ToolCalls = calls
	} else {
		step.Answer = strings.TrimSpace(content.String())
	}
	return step, nil
}

var _ agentkit.LLM = (*Client)(nil)

// toolAcc accumulates streamed tool_call fragments per index and yields them in first-seen order,
// each with its tool_call_id.
type toolAcc struct {
	order []int
	id    map[int]string
	name  map[int]*strings.Builder
	args  map[int]*strings.Builder
}

func newToolAcc() *toolAcc {
	return &toolAcc{
		id:   map[int]string{},
		name: map[int]*strings.Builder{},
		args: map[int]*strings.Builder{},
	}
}

func (a *toolAcc) add(tc goopenai.ToolCall) {
	idx := 0
	if tc.Index != nil {
		idx = *tc.Index
	}
	if _, seen := a.name[idx]; !seen {
		a.order = append(a.order, idx)
		a.name[idx] = &strings.Builder{}
		a.args[idx] = &strings.Builder{}
	}
	if tc.ID != "" {
		a.id[idx] = tc.ID // the id arrives in the first fragment of each call
	}
	a.name[idx].WriteString(tc.Function.Name)
	a.args[idx].WriteString(tc.Function.Arguments)
}

func (a *toolAcc) calls() []agentkit.ToolCall {
	calls := make([]agentkit.ToolCall, 0, len(a.order))
	for _, idx := range a.order {
		id := a.id[idx]
		if id == "" {
			id = fmt.Sprintf("agentkit_call_%d", idx) // some endpoints omit ids; synthesize a stable one
		}
		calls = append(calls, agentkit.ToolCall{
			ID:   id,
			Tool: a.name[idx].String(),
			Args: a.args[idx].String(), // JSON, validated by the tool
		})
	}
	return calls
}

func buildMessages(conv []agentkit.Message) []goopenai.ChatCompletionMessage {
	msgs := make([]goopenai.ChatCompletionMessage, 0, len(conv))
	for _, m := range conv {
		switch m.Role {
		case agentkit.RoleSystem:
			msgs = append(msgs, goopenai.ChatCompletionMessage{Role: goopenai.ChatMessageRoleSystem, Content: m.Content})
		case agentkit.RoleAssistant:
			am := goopenai.ChatCompletionMessage{Role: goopenai.ChatMessageRoleAssistant, Content: m.Content}
			for _, tc := range m.ToolCalls {
				am.ToolCalls = append(am.ToolCalls, goopenai.ToolCall{
					ID:       tc.ID,
					Type:     goopenai.ToolTypeFunction,
					Function: goopenai.FunctionCall{Name: tc.Tool, Arguments: tc.Args},
				})
			}
			msgs = append(msgs, am)
		case agentkit.RoleTool:
			msgs = append(msgs, goopenai.ChatCompletionMessage{
				Role:       goopenai.ChatMessageRoleTool,
				ToolCallID: m.ToolCallID,
				Content:    m.Content,
			})
		default:
			msgs = append(msgs, goopenai.ChatCompletionMessage{Role: goopenai.ChatMessageRoleUser, Content: m.Content})
		}
	}
	return msgs
}
