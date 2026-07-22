package openai

// Internal (white-box) tests for schema.go's unexported renderSchema — per CLAUDE.md §9,
// unexported behavior is tested in the same-package test file.

import (
	"reflect"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
)

func TestRenderSchema(t *testing.T) {
	tests := []struct {
		name string
		in   *agentkit.Schema
		want map[string]any
	}{
		{
			name: "nil renders a bare object",
			in:   nil,
			want: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			name: "object with properties and required",
			in:   agentkit.Object(agentkit.Prop("name", agentkit.String("the name"))).Require("name"),
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "description": "the name"},
				},
				"required": []string{"name"},
			},
		},
		{
			name: "array renders its items",
			in:   agentkit.Array(agentkit.String(""), "a list"),
			want: map[string]any{
				"type":        "array",
				"description": "a list",
				"items":       map[string]any{"type": "string"},
			},
		},
		{
			name: "string enum",
			in:   agentkit.String("a color").WithEnum("red", "green"),
			want: map[string]any{
				"type":        "string",
				"description": "a color",
				"enum":        []string{"red", "green"},
			},
		},
		{
			name: "types stay lowercase",
			in:   agentkit.Integer("a count"),
			want: map[string]any{"type": "integer", "description": "a count"},
		},
		{
			name: "nested object and array",
			in: agentkit.Object(
				agentkit.Prop("tags", agentkit.Array(agentkit.String(""), "labels")),
				agentkit.Prop("meta", agentkit.Object(agentkit.Prop("id", agentkit.Integer("")))),
			).Require("tags"),
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tags": map[string]any{
						"type":        "array",
						"description": "labels",
						"items":       map[string]any{"type": "string"},
					},
					"meta": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id": map[string]any{"type": "integer"},
						},
					},
				},
				"required": []string{"tags"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderSchema(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("renderSchema()\n got = %#v\nwant = %#v", got, tt.want)
			}
		})
	}
}
