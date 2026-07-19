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

	"github.com/efuturetoday/nocturn/internal/activity"
	"github.com/efuturetoday/nocturn/internal/brain"
	"github.com/efuturetoday/nocturn/internal/tool"
)

// Client calls an OpenAI-compatible chat endpoint.
type Client struct {
	api   *openai.Client
	Model string
	// effort is the GLOBAL default reasoning effort (from FREELLM_REASONING_EFFORT), set once in
	// New. A per-turn effort on ctx (brain.EffortFrom) overrides it; "" leaves the request field
	// unset. Unexported: the Client is shared across turn goroutines, so it is read-only after New.
	effort brain.Effort
}

// New returns a Client for an OpenAI-compatible endpoint at baseURL (its /v1 path is appended),
// authenticating with apiKey, using modelName, and defaultEffort as the global reasoning level.
func New(baseURL, apiKey, modelName string, defaultEffort brain.Effort) *Client {
	cfg := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		cfg.BaseURL = strings.TrimRight(baseURL, "/") + "/v1"
	}
	return &Client{api: openai.NewClientWithConfig(cfg), Model: modelName, effort: defaultEffort}
}

// compile-time proof the adapter is a brain.Model.
var _ brain.Model = (*Client)(nil)

// Next sends the conversation and tool schemas to the endpoint and returns the
// model's structured decision: a tool call or a final answer. The completion is
// streamed: answer text is emitted as activity.Token to the activity sink on ctx as
// it arrives (a run with no sink is silent), and tool-call deltas are accumulated
// into a single structured call.
func (c *Client) Next(ctx context.Context, conv []brain.Message, tools []tool.Spec) (brain.Step, error) {
	req := openai.ChatCompletionRequest{
		Model:    c.Model,
		Messages: buildMessages(conv),
		Stream:   true,
	}
	// Reasoning effort: a per-turn value on ctx wins, else the client's global default; "" leaves
	// the field unset (omitempty) so the endpoint decides.
	effort := brain.EffortFrom(ctx)
	if effort == "" {
		effort = c.effort
	}
	req.ReasoningEffort = string(effort)
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

	respStream, err := c.api.CreateChatCompletionStream(ctx, req)
	if err != nil {
		return brain.Step{}, fmt.Errorf("model: %w", err)
	}
	defer respStream.Close()

	var content strings.Builder
	acc := newToolAcc()
	for {
		chunk, err := respStream.Recv()
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
			activity.Emit(ctx, activity.Token{Text: delta.Content})
		}
		// Reasoning ("extended thinking") streams in its own field — surface it as Thinking so a
		// client renders it dim. It is NOT accumulated into the answer.
		if delta.ReasoningContent != "" {
			activity.Emit(ctx, activity.Thinking{Text: delta.ReasoningContent})
		}
		// Tool calls stream in fragments keyed by index: the name arrives once, the
		// arguments in pieces, and several calls interleave. Accumulate PER INDEX,
		// or two parallel calls would be concatenated into one garbage call.
		for _, tc := range delta.ToolCalls {
			acc.add(tc)
		}
	}

	if calls := acc.calls(); len(calls) > 0 {
		return brain.Step{ToolCalls: calls}, nil
	}
	return brain.Step{Answer: strings.TrimSpace(content.String())}, nil
}

// toolAcc accumulates streamed tool_call fragments per index and yields them in
// first-seen order, each with its tool_call_id.
type toolAcc struct {
	order []int
	id    map[int]string
	name  map[int]*strings.Builder
	args  map[int]*strings.Builder
}

func newToolAcc() *toolAcc {
	return &toolAcc{id: map[int]string{}, name: map[int]*strings.Builder{}, args: map[int]*strings.Builder{}}
}

func (a *toolAcc) add(tc openai.ToolCall) {
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

func (a *toolAcc) calls() []brain.ToolCall {
	calls := make([]brain.ToolCall, 0, len(a.order))
	for _, idx := range a.order {
		id := a.id[idx]
		if id == "" {
			id = fmt.Sprintf("nocturn_call_%d", idx) // some endpoints omit ids; synthesize a stable, collision-proof one
		}
		calls = append(calls, brain.ToolCall{
			ID:   id,
			Tool: a.name[idx].String(),
			Args: a.args[idx].String(), // JSON, validated by the tool
		})
	}
	return calls
}

func buildMessages(conv []brain.Message) []openai.ChatCompletionMessage {
	msgs := make([]openai.ChatCompletionMessage, 0, len(conv))
	for _, m := range conv {
		switch m.Role {
		case "system":
			// The standing instruction, seeded by NewConversation (a session's persona
			// or a child agent's Instructions). Transported as-is — the adapter no longer
			// invents one.
			msgs = append(msgs, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: m.Content})
		case "assistant":
			// An assistant turn carries its tool calls natively (tool_calls), so
			// the model sees exactly what it requested — matched to results by id.
			am := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: m.Content}
			for _, tc := range m.ToolCalls {
				am.ToolCalls = append(am.ToolCalls, openai.ToolCall{
					ID:       tc.ID,
					Type:     openai.ToolTypeFunction,
					Function: openai.FunctionCall{Name: tc.Tool, Arguments: tc.Args},
				})
			}
			msgs = append(msgs, am)
		case "tool":
			// A tool result is a native role=tool message tied to its call by id.
			msgs = append(msgs, openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: m.ToolCallID,
				Content:    m.Content,
			})
		default:
			msgs = append(msgs, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: m.Content})
		}
	}
	return msgs
}
