package agentkit_test

import (
	"encoding/json"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
)

func TestParseSchema_RoundTripSupportedSubset(t *testing.T) {
	raw := json.RawMessage(`{
		"type": "object",
		"description": "a thing",
		"minProperties": 2,
		"properties": {
			"name": {"type": "string", "enum": ["a", "b"]},
			"tags": {"type": "array", "items": {"type": "string"}}
		},
		"required": ["name"]
	}`)
	s, err := agentkit.ParseSchema(raw)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	if s.Type != agentkit.TypeObject || s.Description != "a thing" {
		t.Fatalf("type/description = %q/%q", s.Type, s.Description)
	}
	if len(s.Required) != 1 || s.Required[0] != "name" {
		t.Fatalf("required = %v, want [name]", s.Required)
	}
	name := s.Properties["name"]
	if name == nil || name.Type != agentkit.TypeString || len(name.Enum) != 2 {
		t.Fatalf("name property = %+v", name)
	}
	tags := s.Properties["tags"]
	if tags == nil || tags.Type != agentkit.TypeArray || tags.Items == nil || tags.Items.Type != agentkit.TypeString {
		t.Fatalf("tags property = %+v", tags)
	}
	// Unsupported keyword (minProperties) is simply not represented — allowlist by construction.
}

func TestParseSchema_EmptyYieldsNil(t *testing.T) {
	s, err := agentkit.ParseSchema(nil)
	if err != nil || s != nil {
		t.Fatalf("ParseSchema(nil) = (%v, %v), want (nil, nil)", s, err)
	}
}

func TestParseSchema_MalformedError(t *testing.T) {
	if _, err := agentkit.ParseSchema(json.RawMessage(`{bad`)); err == nil {
		t.Fatal("ParseSchema of malformed JSON: err = nil, want error")
	}
}

func TestObject_Prop_Require_Chaining(t *testing.T) {
	s := agentkit.Object(
		agentkit.Prop("a", agentkit.String("d1")),
		agentkit.Prop("b", agentkit.Number("d2")),
	).Require("a")
	if s.Type != agentkit.TypeObject {
		t.Fatalf("type = %q", s.Type)
	}
	if len(s.Properties) != 2 || s.Properties["a"] == nil || s.Properties["b"] == nil {
		t.Fatalf("properties = %+v", s.Properties)
	}
	if len(s.Required) != 1 || s.Required[0] != "a" {
		t.Fatalf("required = %v, want [a]", s.Required)
	}
}

func TestString_WithEnum(t *testing.T) {
	s := agentkit.String("choose").WithEnum("x", "y", "z")
	if s.Type != agentkit.TypeString || len(s.Enum) != 3 || s.Enum[0] != "x" {
		t.Fatalf("enum schema = %+v", s)
	}
}
