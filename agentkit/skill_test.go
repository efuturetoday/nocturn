package agentkit

import "testing"

// TestSkillLoad_Name asserts the progressive-disclosure tool is named skill_load (the skill_* family
// shared with a consumer's skill_read).
func TestSkillLoad_Name(t *testing.T) {
	set, err := NewSkillSet(Skill{Name: "x", Description: "d", Body: "b"})
	if err != nil {
		t.Fatalf("NewSkillSet: %v", err)
	}
	if got := set.LoadTool().Spec().Name; got != "skill_load" {
		t.Fatalf("LoadTool name = %q, want skill_load", got)
	}
}
