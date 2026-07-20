package agentkit

import (
	"encoding/json"
	"fmt"
)

// SchemaType is a JSON Schema primitive type.
type SchemaType string

const (
	TypeObject  SchemaType = "object"
	TypeString  SchemaType = "string"
	TypeNumber  SchemaType = "number"
	TypeInteger SchemaType = "integer"
	TypeBoolean SchemaType = "boolean"
	TypeArray   SchemaType = "array"
)

// Schema is agentkit's canonical, provider-agnostic parameter schema. It is the ONE representation
// of a tool's arguments: build it with the helpers below, or parse a foreign JSON Schema into it
// with ParseSchema — either way you get a Schema. An adapter renders it to a provider's accepted
// JSON Schema. There is nothing to "sanitize": the model only expresses supported constructs, so
// unsupported keywords simply have no field and never reach a provider (allowlist by construction).
type Schema struct {
	Type        SchemaType
	Description string
	Properties  map[string]*Schema // for Object
	Required    []string           // for Object
	Items       *Schema            // for Array
	Enum        []string           // for String
}

// Field is one named property of an Object schema.
type Field struct {
	Name   string
	Schema *Schema
}

// Prop names a property for Object.
func Prop(name string, schema *Schema) Field { return Field{Name: name, Schema: schema} }

// Object builds an object schema from its properties. Chain .Require to mark required ones.
func Object(props ...Field) *Schema {
	s := &Schema{Type: TypeObject, Properties: make(map[string]*Schema, len(props))}
	for _, p := range props {
		s.Properties[p.Name] = p.Schema
	}
	return s
}

// Require marks properties as required and returns the schema (for chaining).
func (s *Schema) Require(names ...string) *Schema {
	s.Required = names
	return s
}

// WithEnum restricts a string schema to a set of values and returns it (for chaining).
func (s *Schema) WithEnum(values ...string) *Schema {
	s.Enum = values
	return s
}

func String(description string) *Schema  { return &Schema{Type: TypeString, Description: description} }
func Number(description string) *Schema  { return &Schema{Type: TypeNumber, Description: description} }
func Integer(description string) *Schema { return &Schema{Type: TypeInteger, Description: description} }
func Bool(description string) *Schema    { return &Schema{Type: TypeBoolean, Description: description} }

// Array builds an array schema over an item schema.
func Array(items *Schema, description string) *Schema {
	return &Schema{Type: TypeArray, Description: description, Items: items}
}

// ParseSchema maps a foreign JSON Schema into a Schema, keeping the supported constructs (type,
// description, properties, required, items, string enum) and dropping the rest by simply not
// representing it — so an MCP/plugin tool's hand-written schema goes through the same render path,
// no blocklist. An empty input yields nil (a no-arg tool). Malformed JSON is an error.
func ParseSchema(raw json.RawMessage) (*Schema, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var node map[string]any
	if err := json.Unmarshal(raw, &node); err != nil {
		return nil, fmt.Errorf("agentkit: parse schema: %w", err)
	}
	return parseNode(node), nil
}

func parseNode(node map[string]any) *Schema {
	s := &Schema{}
	if t, ok := node["type"].(string); ok {
		s.Type = SchemaType(t)
	}
	if d, ok := node["description"].(string); ok {
		s.Description = d
	}
	if props, ok := node["properties"].(map[string]any); ok {
		s.Properties = make(map[string]*Schema, len(props))
		for name, v := range props {
			if pm, ok := v.(map[string]any); ok {
				s.Properties[name] = parseNode(pm)
			}
		}
	}
	if req, ok := node["required"].([]any); ok {
		for _, r := range req {
			if rs, ok := r.(string); ok {
				s.Required = append(s.Required, rs)
			}
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		s.Items = parseNode(items)
	}
	if enum, ok := node["enum"].([]any); ok {
		for _, e := range enum {
			if es, ok := e.(string); ok {
				s.Enum = append(s.Enum, es)
			}
		}
	}
	return s
}
