package agentkit_test

import (
	"context"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
)

func TestSkill_Validate(t *testing.T) {
	long := strings.Repeat("d", agentkit.MaxSkillDescLen+1)
	tests := []struct {
		name    string
		skill   agentkit.Skill
		wantErr bool
	}{
		{"valid", agentkit.Skill{Name: "pdf-tools", Description: "d"}, false},
		{"bad name uppercase", agentkit.Skill{Name: "PDF", Description: "d"}, true},
		{"bad name underscore", agentkit.Skill{Name: "pdf_tools", Description: "d"}, true},
		{"reserved anthropic", agentkit.Skill{Name: "anthropic-x", Description: "d"}, true},
		{"reserved claude", agentkit.Skill{Name: "claude-x", Description: "d"}, true},
		{"empty description", agentkit.Skill{Name: "pdf", Description: ""}, true},
		{"overlong description", agentkit.Skill{Name: "pdf", Description: long}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.skill.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate(%+v) err = %v, wantErr %v", tt.skill, err, tt.wantErr)
			}
		})
	}
}

func TestNewSkillSet_DuplicateName_Error(t *testing.T) {
	_, err := agentkit.NewSkillSet(
		agentkit.Skill{Name: "dup", Description: "a"},
		agentkit.Skill{Name: "dup", Description: "b"},
	)
	if err == nil {
		t.Fatal("duplicate skill name: err = nil, want error")
	}
}

func TestSkillSet_LoadTool_ReturnsBody(t *testing.T) {
	set, err := agentkit.NewSkillSet(agentkit.Skill{Name: "pdf", Description: "d", Body: "the body"})
	if err != nil {
		t.Fatalf("NewSkillSet: %v", err)
	}
	load := set.LoadTool()

	t.Run("known skill returns body", func(t *testing.T) {
		out, err := load.Call(context.Background(), `{"name":"pdf"}`)
		if err != nil {
			t.Fatalf("Call err = %v", err)
		}
		if out != "the body" {
			t.Fatalf("body = %q, want %q", out, "the body")
		}
	})
	t.Run("unknown skill errors", func(t *testing.T) {
		if _, err := load.Call(context.Background(), `{"name":"nope"}`); err == nil {
			t.Fatal("unknown skill: err = nil, want error")
		}
	})
	t.Run("malformed args error", func(t *testing.T) {
		if _, err := load.Call(context.Background(), `{bad`); err == nil {
			t.Fatal("malformed args: err = nil, want error")
		}
	})
}

func TestSkillSet_Specs_SortedBodiesOmitted(t *testing.T) {
	set, err := agentkit.NewSkillSet(
		agentkit.Skill{Name: "zebra", Description: "z", Body: "zbody"},
		agentkit.Skill{Name: "alpha", Description: "a", Body: "abody"},
	)
	if err != nil {
		t.Fatalf("NewSkillSet: %v", err)
	}
	specs := set.Specs()
	if len(specs) != 2 || specs[0].Name != "alpha" || specs[1].Name != "zebra" {
		t.Fatalf("specs not sorted: %+v", specs)
	}
	for _, s := range specs {
		if s.Body != "" {
			t.Fatalf("spec %q leaks a body: %q", s.Name, s.Body)
		}
	}
}

// TestSkillLoad_Name asserts the progressive-disclosure tool is named skill_load (the skill_* family
// shared with a consumer's skill_read).
func TestSkillLoad_Name(t *testing.T) {
	set, err := agentkit.NewSkillSet(agentkit.Skill{Name: "x", Description: "d", Body: "b"})
	if err != nil {
		t.Fatalf("NewSkillSet: %v", err)
	}
	if got := set.LoadTool().Spec().Name; got != "skill_load" {
		t.Fatalf("LoadTool name = %q, want skill_load", got)
	}
}
