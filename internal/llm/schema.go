package llm

import "encoding/json"

// Tool schemas are JSON Schema, which OpenAI accepts nearly whole. But an OpenAI-compatible proxy
// may route to a provider whose function-calling schema is a STRICT OpenAPI-3.0 subset (notably
// Google Gemini): enum values must be strings, `type` is a single value (no unions), and many
// JSON Schema keywords simply do not exist. A schema authored/generated for OpenAI — commonly a
// third-party MCP or plugin tool — then fails on that provider. sanitizeSchema normalizes each
// tool's Parameters to the lowest common denominator BEFORE sending, so a tool works regardless
// of which model the proxy picks. It returns the (possibly rewritten) schema and the list of
// changes it made (for a diagnostic log naming the offending tool).

// geminiUnsupportedKeys are JSON Schema keywords the strict subset rejects (OpenAI tolerates
// them). They are stripped; the property keeps its type/description.
var geminiUnsupportedKeys = []string{
	"$schema", "$id", "$ref", "$comment", "$defs", "definitions",
	"additionalProperties", "patternProperties", "unevaluatedProperties",
	"title", "default", "examples", "const",
}

// sanitizeSchema rewrites raw to the strict-subset-safe form. Non-JSON input passes through
// unchanged (nothing to walk).
func sanitizeSchema(raw json.RawMessage) (json.RawMessage, []string) {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw, nil
	}
	var fixes []string
	v = sanitizeNode(v, &fixes)
	if len(fixes) == 0 {
		return raw, nil // untouched — keep the original bytes
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw, nil // should not happen (we only trimmed) — fail safe, send the original
	}
	return out, fixes
}

// sanitizeNode fixes one schema node in place and recurses into its children.
func sanitizeNode(v any, fixes *[]string) any {
	switch n := v.(type) {
	case map[string]any:
		for _, k := range geminiUnsupportedKeys {
			if _, ok := n[k]; ok {
				delete(n, k)
				*fixes = append(*fixes, "dropped "+k)
			}
		}
		// `type` as a union array (["string","null"]) → a single type + nullable.
		if arr, ok := n["type"].([]any); ok {
			single, nullable := "", false
			for _, e := range arr {
				if s, _ := e.(string); s == "null" {
					nullable = true
				} else if single == "" {
					single = s
				}
			}
			n["type"] = single
			if nullable {
				n["nullable"] = true
			}
			*fixes = append(*fixes, "flattened type union")
		}
		// enum with any non-string value → drop the enum (the type still constrains it).
		if e, ok := n["enum"].([]any); ok {
			for _, x := range e {
				if _, isStr := x.(string); !isStr {
					delete(n, "enum")
					*fixes = append(*fixes, "dropped non-string enum")
					break
				}
			}
		}
		// combinators the subset does not support — best-effort drop (avoids a hard reject).
		for _, k := range []string{"anyOf", "oneOf", "allOf", "not", "if", "then", "else"} {
			if _, ok := n[k]; ok {
				delete(n, k)
				*fixes = append(*fixes, "dropped "+k)
			}
		}
		for k, child := range n {
			n[k] = sanitizeNode(child, fixes)
		}
		return n
	case []any:
		for i := range n {
			n[i] = sanitizeNode(n[i], fixes)
		}
		return n
	default:
		return v
	}
}
