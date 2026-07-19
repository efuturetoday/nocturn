package llm

import (
	"encoding/json"
	"reflect"
	"testing"
)

// asMap unmarshals a schema for structural assertions.
func asMap(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, raw)
	}
	return m
}

func TestSanitizeSchema_ConformantUntouched(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"method":{"type":"string","enum":["GET","HEAD"]}},"required":["method"]}`)
	out, fixes := sanitizeSchema(raw)
	if len(fixes) != 0 {
		t.Fatalf("fixes = %v, want none for a conformant schema", fixes)
	}
	if !reflect.DeepEqual([]byte(out), []byte(raw)) {
		t.Fatalf("conformant schema was rewritten: %s", out)
	}
}

// The exact failure from the field: a nested array-of-objects with a boolean enum, plus the
// unsupported keywords a strict provider rejects.
func TestSanitizeSchema_FixesStrictSubsetViolations(t *testing.T) {
	raw := json.RawMessage(`{
		"$schema":"http://json-schema.org/draft-07/schema#",
		"type":"object",
		"additionalProperties":false,
		"properties":{
			"a":{"type":"string","default":"x"},
			"b":{"type":["string","null"]},
			"c":{"anyOf":[{"type":"string"},{"type":"number"}]},
			"items":{"type":"array","items":{"type":"object","properties":{
				"flag":{"enum":[true,false]}
			}}}
		}
	}`)
	out, fixes := sanitizeSchema(raw)
	if len(fixes) == 0 {
		t.Fatal("expected fixes, got none")
	}
	m := asMap(t, out)

	if _, ok := m["$schema"]; ok {
		t.Error("$schema not dropped")
	}
	if _, ok := m["additionalProperties"]; ok {
		t.Error("additionalProperties not dropped")
	}
	props := m["properties"].(map[string]any)
	if _, ok := props["a"].(map[string]any)["default"]; ok {
		t.Error("default not dropped")
	}
	// type union → single type + nullable.
	b := props["b"].(map[string]any)
	if b["type"] != "string" || b["nullable"] != true {
		t.Errorf("type union not flattened: %v", b)
	}
	// anyOf dropped.
	if _, ok := props["c"].(map[string]any)["anyOf"]; ok {
		t.Error("anyOf not dropped")
	}
	// nested boolean enum dropped (this was the actual API-rejected construct).
	flag := props["items"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["flag"].(map[string]any)
	if _, ok := flag["enum"]; ok {
		t.Errorf("nested boolean enum not dropped: %v", flag)
	}
}

func TestSanitizeSchema_NonJSONPassesThrough(t *testing.T) {
	raw := json.RawMessage(`not json`)
	out, fixes := sanitizeSchema(raw)
	if fixes != nil || !reflect.DeepEqual([]byte(out), []byte(raw)) {
		t.Fatalf("non-JSON should pass through untouched, got out=%s fixes=%v", out, fixes)
	}
}
