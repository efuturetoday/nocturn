package agent_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/agent"
)

func writeAgent(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAgents_Valid(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "morning-brief.md", `---
name: morning-brief
description: Summarize my morning
when: cron("0 7 * * *")
tools: [gmail, github.search]
budget: 5m
---
Read my unread mail and summarize it.
`)
	defs, err := agent.LoadAgents(dir)
	if err != nil || len(defs) != 1 {
		t.Fatalf("defs=%+v err=%v", defs, err)
	}
	d := defs[0]
	if d.Name != "morning-brief" || d.Description != "Summarize my morning" || d.When != `cron("0 7 * * *")` || d.Budget != 5*time.Minute {
		t.Fatalf("parsed wrong: %+v", d)
	}
	if len(d.Tools) != 2 || d.Tools[0] != "gmail" || d.Tools[1] != "github.search" {
		t.Fatalf("tools = %v", d.Tools)
	}
	if d.Instructions != "Read my unread mail and summarize it." {
		t.Fatalf("instructions = %q", d.Instructions)
	}
}

// name defaults to the filename; when defaults to "manual".
func TestLoadAgents_Defaults(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "helper.md", "---\ntools: [file]\n---\nDo a thing.\n")
	defs, err := agent.LoadAgents(dir)
	if err != nil || len(defs) != 1 {
		t.Fatalf("defs=%+v err=%v", defs, err)
	}
	if defs[0].Name != "helper" || defs[0].When != "manual" || defs[0].Budget != 0 {
		t.Fatalf("defaults wrong: %+v", defs[0])
	}
}

func TestLoadAgents_MissingDir(t *testing.T) {
	defs, err := agent.LoadAgents(filepath.Join(t.TempDir(), "absent"))
	if err != nil || defs != nil {
		t.Fatalf("defs=%v err=%v, want nil,nil", defs, err)
	}
}

func TestLoadAgents_FailClosed(t *testing.T) {
	cases := map[string]string{
		"no frontmatter": "just a body, no ---\n",
		"unterminated":   "---\nname: x\ntools: [file]\nbody without closer\n",
		"no tools":       "---\nname: x\n---\nbody\n",
		"empty body":     "---\nname: x\ntools: [file]\n---\n   \n",
		"bad name":       "---\nname: Bad Name!\ntools: [file]\n---\nbody\n",
		"bad budget":     "---\nname: x\ntools: [file]\nbudget: 5 lightyears\n---\nbody\n",
		"bad yaml":       "---\ntools: [unclosed\n---\nbody\n",
	}
	for label, content := range cases {
		t.Run(label, func(t *testing.T) {
			dir := t.TempDir()
			writeAgent(t, dir, "x.md", content)
			if _, err := agent.LoadAgents(dir); err == nil {
				t.Fatalf("LoadAgents accepted %s", label)
			}
		})
	}
}

func TestLoadAgents_DuplicateName(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "a.md", "---\nname: dup\ntools: [file]\n---\nbody\n")
	writeAgent(t, dir, "b.md", "---\nname: dup\ntools: [file]\n---\nbody\n")
	if _, err := agent.LoadAgents(dir); err == nil {
		t.Fatal("duplicate agent name accepted")
	}
}

func TestDefinition_Matches(t *testing.T) {
	// Mix bare group, exact tool, and both wildcard spellings (VS Code "/*" and ".*").
	d := agent.Definition{Tools: []string{"gmail", "github.search", "linear.*", "notion/*"}}
	for name, want := range map[string]bool{
		"gmail.search":  true,  // bare-group prefix
		"gmail.send":    true,  // bare-group prefix
		"gmail":         true,  // exact group name
		"github.search": true,  // exact
		"github.create": false, // sibling not listed
		"linear.issue":  true,  // ".*" wildcard
		"notion.page":   true,  // "/*" wildcard
		"file.read":     false, // other group
		"gmailx.evil":   false, // not a prefix boundary
	} {
		if got := d.Matches(name); got != want {
			t.Errorf("Matches(%q) = %v, want %v", name, got, want)
		}
	}
}
