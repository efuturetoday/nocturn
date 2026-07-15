package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/efuturetoday/nocturn/internal/tool"
)

// LoadToolName is the single meta-tool through which the model activates skills.
// It is NOT a per-skill tool — skills are never tools; this one activation door
// preserves that while giving the model model-driven progressive disclosure.
const LoadToolName = "skill.load"

// metaInvocationKey is the recognized metadata key; value "never" hides a skill
// from the catalog + enum (model can't auto-load it), leaving it reachable only
// by explicit user invocation (/name).
const metaInvocationKey = "nocturn.model-invocation"

// skillBodyBudget lets a loaded skill body through the brain's per-result
// truncation (which defaults to a few KB) — a skill is durable instruction text,
// not a bounded tool output.
const skillBodyBudget = 64 << 10

// visible returns the skills the model may auto-load (excludes those the author
// marked model-invocation: never).
func (ix *Index) visible() []Skill {
	out := make([]Skill, 0, len(ix.order))
	for _, s := range ix.Skills() {
		if s.Metadata[metaInvocationKey] != "never" {
			out = append(out, s)
		}
	}
	return out
}

// LoadTool builds the skill.load meta-tool: its Description carries the Tier-1
// catalog (each skill's name + description), its single "name" parameter is an
// enum of the visible skill names (so the model can't invent a skill), and its
// Invoke returns the chosen skill's body wrapped for the model, deduplicated
// against the session's activation set. Returns ok=false if no skill is visible
// (the caller then registers nothing — no empty tool).
func (ix *Index) LoadTool() (tool.Tool, bool) {
	vis := ix.visible()
	if len(vis) == 0 {
		return tool.Tool{}, false
	}

	var catalog strings.Builder
	catalog.WriteString("Load a skill's full instructions into the conversation when it is relevant " +
		"to the user's request, then follow them. A skill is guidance, not an action; every effect it " +
		"leads to still goes through the normal tools. Available skills:\n")
	names := make([]string, 0, len(vis))
	for _, s := range vis {
		catalog.WriteString("- " + s.Name + ": " + s.Description + "\n")
		names = append(names, s.Name)
	}

	params, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "The skill to load.",
				"enum":        names,
			},
		},
		"required": []string{"name"},
	})

	return tool.Tool{
		Spec: tool.Spec{
			Name:        LoadToolName,
			Description: catalog.String(),
			Parameters:  params,
			MaxResult:   skillBodyBudget,
		},
		Invoke: func(ctx context.Context, args string) (string, error) {
			var a struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal([]byte(args), &a); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			s, ok := ix.Get(a.Name)
			if !ok || s.Metadata[metaInvocationKey] == "never" {
				return "", fmt.Errorf("unknown skill %q", a.Name)
			}
			if act := ActiveFrom(ctx); act != nil && !act.Mark(a.Name) {
				return "skill " + a.Name + " is already loaded in this conversation.", nil
			}
			body, err := s.Body()
			if err != nil {
				return "", err
			}
			return WrapBody(a.Name, body), nil
		},
	}, true
}

// WrapBody frames a skill body so the model can tell instruction content apart
// from conversation. Exported so the explicit /name path (later) wraps identically.
func WrapBody(name, body string) string {
	return "<skill name=\"" + name + "\">\n" + strings.TrimSpace(body) + "\n</skill>"
}
