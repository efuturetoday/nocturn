package openai

import "encoding/json"

// sanitizeSchema normalizes a tool's JSON Schema to the strict lowest-common-denominator
// subset some providers (Gemini-strict) accept, without dropping a parameter merely
// because it is named like a JSON Schema keyword.
func sanitizeSchema(raw json.RawMessage) json.RawMessage { panic("TODO") }
