package gemini

import (
	"strings"

	"github.com/efuturetoday/nocturn/agentkit"
)

// renderSchema maps agentkit's canonical Schema to the OpenAPI subset Gemini's functionDeclarations
// accept. It is the openai adapter's renderSchema with one difference that matters: Gemini's type
// enum is UPPERCASE ("OBJECT", "STRING"), where an OpenAI-compatible endpoint wants lowercase.
// Because agentkit's Schema only holds the portable subset shared by OpenAI, Anthropic and Gemini,
// there is nothing to strip — the output is the safe lowest common denominator by construction.
//
// A nil schema renders as a bare object: a no-argument tool still needs a parameters block.
func renderSchema(s *agentkit.Schema) map[string]any {
	if s == nil {
		return map[string]any{"type": "OBJECT", "properties": map[string]any{}}
	}
	m := map[string]any{}
	if s.Type != "" {
		m["type"] = strings.ToUpper(string(s.Type))
	}
	if s.Description != "" {
		m["description"] = s.Description
	}
	if len(s.Properties) > 0 {
		props := make(map[string]any, len(s.Properties))
		for name, p := range s.Properties {
			props[name] = renderSchema(p)
		}
		m["properties"] = props
	}
	if len(s.Required) > 0 {
		m["required"] = s.Required
	}
	if s.Items != nil {
		m["items"] = renderSchema(s.Items)
	}
	if len(s.Enum) > 0 {
		m["enum"] = s.Enum
	}
	return m
}

// nonBlocking is declared on every tool, and that is a deliberate blanket choice.
//
// Gemini's default is blocking: the API pauses ALL model interaction until a functionResponse
// arrives. For a gated deployment that is unusable — a call waiting on a human's approval on
// another device would freeze the conversation for as long as the human takes, with no way for the
// model to even say so. Running the call in its own goroutine does not help: it keeps the audio
// transport moving, but the model on the far side is still stopped.
//
// The blanket applies because the consumer cannot know in advance which call will block. Whether a
// tool asks is decided at call time by the policy and the remembered grants, not by the tool's
// identity — so any tool may turn out to be the slow one, and declaring only the "slow" ones
// non-blocking would be guessing.
const nonBlocking = "NON_BLOCKING"

// declare renders the tool specs into the single Tools entry Gemini expects.
func declare(tools []agentkit.ToolSpec) []toolDecl {
	if len(tools) == 0 {
		return nil
	}
	decls := make([]functionDecl, 0, len(tools))
	for _, t := range tools {
		decls = append(decls, functionDecl{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  renderSchema(t.Parameters),
			Behavior:    nonBlocking,
		})
	}
	return []toolDecl{{FunctionDeclarations: decls}}
}
