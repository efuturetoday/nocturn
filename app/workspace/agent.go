package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/efuturetoday/nocturn/agentkit"
)

// Agent is a declared assistant: a scoped persona that runs on a schedule or on demand. Its
// authority is its tool cage (Tools → ToolSet.Select) plus the workspace gate; an unattended firing
// gets no approver, so anything not already granted is denied.
type Agent struct {
	Name         string
	Instructions string
	Tools        []string        // tool-name filter for the cage; empty = a pure reasoner
	When         string          // cron schedule; "" = manual only
	Effort       agentkit.Effort // reasoning effort for the agent's runs
}

// Matches reports whether toolName is in the agent's cage. A bare group name ("http") also matches
// its members ("http.read", "http/get").
func (a Agent) Matches(toolName string) bool {
	for _, t := range a.Tools {
		if t == toolName || strings.HasPrefix(toolName, t+".") || strings.HasPrefix(toolName, t+"/") {
			return true
		}
	}
	return false
}

// agentFrontmatter is the YAML head of an agent.md; the markdown body below it is the Instructions.
type agentFrontmatter struct {
	Name   string   `yaml:"name"`
	Tools  []string `yaml:"tools"`
	When   string   `yaml:"when"`
	Effort string   `yaml:"effort"`
}

// discoverAgents reads <dir>/agents/<name>/agent.md for every subdirectory. A missing agents dir
// yields none (not an error).
func discoverAgents(dir string) ([]Agent, error) {
	root := filepath.Join(dir, "agents")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var agents []Agent
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		a, err := readAgent(filepath.Join(root, e.Name(), "agent.md"))
		if err != nil {
			return nil, err
		}
		if a != nil {
			agents = append(agents, *a)
		}
	}
	return agents, nil
}

// readAgent parses one agent.md into an Agent, or (nil, nil) if the file is absent.
func readAgent(path string) (*Agent, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	head, body := splitFrontmatter(data)
	var fm agentFrontmatter
	if err := yaml.Unmarshal(head, &fm); err != nil {
		return nil, fmt.Errorf("agent %s: %w", path, err)
	}
	if fm.Name == "" {
		return nil, fmt.Errorf("agent %s: missing name", path)
	}
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
