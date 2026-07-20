package openai

import "encoding/json"

// strictDrop lists JSON Schema keywords that strict providers (e.g. Gemini behind a proxy) reject.
// They are stripped from every schema-object position — but never from property NAMES (a property
// may legitimately be named "default" or "title"; see sanitizeSchema's handling of "properties").
var strictDrop = map[string]bool{
	"$schema":              true,
	"$id":                  true,
	"$defs":                true,
	"definitions":          true,
	"additionalProperties": true,
	"title":                true,
	"default":              true,
	"examples":             true,
}

// sanitizeSchema normalizes a tool's JSON Schema to the strict lowest-common-denominator subset some
// providers accept, returning the cleaned schema and whether anything changed. An empty schema
// becomes a bare object; malformed JSON is passed through unchanged (Validate already rejects it).
func sanitizeSchema(raw json.RawMessage) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`), false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw, false
	}
	changed := false
	cleaned := scrub(v, &changed)
	if !changed {
		return raw, false
	}
	out, err := json.Marshal(cleaned)
	if err != nil {
		return raw, false
	}
	return out, true
}

// scrub recursively drops strictDrop keywords. Under a "properties" object it treats keys as
// property names (not keywords) and only scrubs their schema values.
func scrub(v any, changed *bool) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, val := range t {
			if strictDrop[k] {
				*changed = true
				continue
			}
			if k == "properties" {
				if props, ok := val.(map[string]any); ok {
					np := make(map[string]any, len(props))
					for name, schema := range props {
						np[name] = scrub(schema, changed)
					}
					m[k] = np
					continue
				}
			}
			m[k] = scrub(val, changed)
		}
		return m
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = scrub(e, changed)
		}
		return out
	default:
		return v
	}
}
