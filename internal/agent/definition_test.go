package agent_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/agent"
	"github.com/efuturetoday/nocturn/internal/capability"
)

// writeAgent creates <dir>/<name>/agent.md — an agent is a self-contained folder.
func writeAgent(t *testing.T, dir, name, content string) {
	t.Helper()
	folder := filepath.Join(dir, name)
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "agent.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAgents_Valid(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "morning-brief", `---
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

// name defaults to the folder name; when defaults to "manual".
func TestLoadAgents_Defaults(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "helper", "---\ntools: [file]\n---\nDo a thing.\n")
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
			writeAgent(t, dir, "x", content)
			if _, err := agent.LoadAgents(dir); err == nil {
				t.Fatalf("LoadAgents accepted %s", label)
			}
		})
	}
}

func TestLoadAgents_DuplicateName(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "a", "---\nname: dup\ntools: [file]\n---\nbody\n")
	writeAgent(t, dir, "b", "---\nname: dup\ntools: [file]\n---\nbody\n")
	if _, err := agent.LoadAgents(dir); err == nil {
		t.Fatal("duplicate agent name accepted")
	}
}

// An agent author can declare its own policy (deny/ask, tightening) and cage.
func TestLoadAgents_PolicyAndCage(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "triage", `---
name: triage
tools: [http.read, http.write]
policy:
  - { effect: deny, family: http, target: "*.internal.corp", access: [write] }
  - { effect: ask,  family: file, target: "*",              access: [read] }
cage:
  - { family: http, target: "api.github.com", access: [read, write] }
---
Triage the inbox.
`)
	defs, err := agent.LoadAgents(dir)
	if err != nil || len(defs) != 1 {
		t.Fatalf("defs=%+v err=%v", defs, err)
	}
	d := defs[0]
	if len(d.Policy.Rules) != 2 {
		t.Fatalf("policy rules = %+v, want 2", d.Policy.Rules)
	}
	if d.Policy.Rules[0].Effect != capability.Deny || d.Policy.Rules[0].Writes != capability.MatchWrite {
		t.Fatalf("rule[0] = %+v, want deny/write", d.Policy.Rules[0])
	}
	if len(d.Cage) != 1 || d.Cage[0].Writes != capability.MatchAny {
		t.Fatalf("cage = %+v, want one read+write pair", d.Cage)
	}
}

// "allow" in an agent policy is rejected — loosening is the deferred autonomy dial,
// not a silent no-op.
func TestLoadAgents_PolicyAllowRejected(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "loose", "---\nname: loose\ntools: [http.write]\npolicy:\n  - { effect: allow, family: http, target: \"*\" }\n---\nbody\n")
	if _, err := agent.LoadAgents(dir); err == nil {
		t.Fatal("policy effect \"allow\" (loosening) must be rejected")
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
