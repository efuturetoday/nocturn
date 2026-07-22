package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/app/discovery"
)

// Discover reads dir/<name>/agent.md for every subdirectory into a Set. A missing dir yields an
// empty Set. A malformed agent.md is SKIPPED with a diagnostic rather than aborting the scan — a
// broken agent's authority is then simply absent (fail-closed), and the other agents still load. A
// duplicate name keeps the first (shadowing).
func Discover(dir string, diag *agentkit.Diagnostics) Set {
	set := Set{}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return set
	}
	if err != nil {
		discovery.Diagnose(diag, "agent", "read dir "+dir+": "+err.Error())
		return set
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		a, err := loadAgent(filepath.Join(dir, e.Name(), "agent.md"))
		if err != nil {
			discovery.Diagnose(diag, "agent:"+e.Name(), err.Error())
			continue
		}
		if a == nil {
			continue // subfolder without an agent.md — not an agent
		}
		// Identity is the folder; a frontmatter name is advisory (mismatch warns, folder wins).
		name, ok := discovery.ResolveName(diag, "agent", e.Name(), a.Name)
		if !ok {
			continue // folder name not a valid identifier
		}
		a.Name = name
		if _, dup := set[a.Name]; dup {
			discovery.Diagnose(diag, "agent:"+a.Name, "skipped (duplicate name; first wins)")
			continue
		}
		set[a.Name] = *a
	}
	return set
}

// frontmatter is the YAML head of an agent.md; the markdown body below it is the Instructions.
type frontmatter struct {
	Name   string   `yaml:"name"`
	Tools  []string `yaml:"tools"`
	When   string   `yaml:"when"`
	Effort string   `yaml:"effort"`
}

// loadAgent parses one agent.md into an Agent, or (nil, nil) if the file is absent.
func loadAgent(path string) (*Agent, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	head, body := splitFrontmatter(data)
	var fm frontmatter
	if err := yaml.Unmarshal(head, &fm); err != nil {
		return nil, fmt.Errorf("agent %s: %w", path, err)
	}
	// The name is optional here: Discover resolves it (a frontmatter name overrides
	// the folder name, else the folder name is used).
	return &Agent{
		Name:         fm.Name,
		Instructions: strings.TrimSpace(string(body)),
		Tools:        fm.Tools,
		When:         fm.When,
		Effort:       agentkit.Effort(fm.Effort),
	}, nil
}

// splitFrontmatter splits a "---\n<yaml>\n---\n<body>" document into its yaml head and markdown body.
// Without a leading "---" the whole input is the body (no frontmatter).
func splitFrontmatter(data []byte) (head, body []byte) {
	s := strings.TrimLeft(string(data), " \t\r\n")
	if !strings.HasPrefix(s, "---") {
		return nil, []byte(s)
	}
	rest := s[len("---"):]
	h, after, ok := strings.Cut(rest, "\n---")
	if !ok {
		return []byte(rest), nil // unterminated: treat all as head
	}
	// after is the remainder of the closing "---" line; the body starts on the next line.
	_, b, _ := strings.Cut(after, "\n")
	return []byte(h), []byte(b)
}
