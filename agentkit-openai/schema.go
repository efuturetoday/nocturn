package openai

import "github.com/efuturetoday/agentkit"

// renderSchema maps agentkit's canonical Schema to the JSON Schema the endpoint accepts. Because the
// Schema model only holds the portable subset (type/description/properties/required/items/enum) —
// the intersection supported by OpenAI, Anthropic and Gemini — the output is inherently the safe
// lowest-common-denominator: there is nothing to strip. Types stay lowercase (an OpenAI-compatible
// endpoint expects that; a proxy fronting Gemini uppercases them). A nil schema is a bare object.
func renderSchema(s *agentkit.Schema) map[string]any {
	if s == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	m := map[string]any{}
	if s.Type != "" {
		m["type"] = string(s.Type)
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
