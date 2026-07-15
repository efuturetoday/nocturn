package agent

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Definition is a workspace agent: WHAT it does (Instructions = the markdown
// body), WHICH tools it may use (Tools, by group name "gmail" or exact name
// "github.search"), and WHEN it runs (When). It is loaded from
// <ws>/.agents/<name>.md — control-plane (outside the model's mount, ADR-10), so
// the model cannot author its own agents. Tools are the ONLY authority surface;
// skills (context, zero authority) are opt-in via the tools list (add "skill") —
// off by default so a focused agent carries no skill-catalog context.
type Definition struct {
	Name         string
	Description  string        // one-line summary (shown when listing/selecting agents)
	Instructions string        // the markdown body — the agent's task framing
	Model        string        // optional model override; "" = default
	Tools        []string      // group ("gmail") or exact ("github.search") tool names
	When         string        // "manual" | a cron expr | "webhook" (v1: manual is what runs)
	Budget       time.Duration // wall-clock budget per run; 0 = caller default
}

// Matches reports whether a registry tool name is one this agent may use. A list
// entry is an exact tool name ("github.search") or a GROUP: "gmail", "gmail.*",
// or "gmail/*" (the VS Code style) all match every "gmail.*" tool.
func (d Definition) Matches(toolName string) bool {
	for _, t := range d.Tools {
		group := strings.TrimSuffix(strings.TrimSuffix(t, "/*"), ".*")
		if toolName == t || toolName == group || strings.HasPrefix(toolName, group+".") {
			return true
		}
	}
	return false
}

type frontmatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Model       string   `yaml:"model"`
	Tools       []string `yaml:"tools"`
	When        string   `yaml:"when"`
	Budget      string   `yaml:"budget"` // Go duration, e.g. "5m"; "" = default
}

const maxAgentBytes = 64 << 10

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// LoadAgents reads every <dir>/*.md agent definition. A missing dir yields no
// agents (nil, nil). A malformed file is an error naming the file, fail-closed —
// the operator never gets a half-understood agent. Returned sorted by name.
func LoadAgents(dir string) ([]Definition, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("agent: read %s: %w", dir, err)
	}
	var defs []Definition
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		def, err := loadAgent(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if seen[def.Name] {
			return nil, fmt.Errorf("agent: duplicate name %q", def.Name)
		}
		seen[def.Name] = true
		defs = append(defs, def)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs, nil
}

func loadAgent(path string) (Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Definition{}, fmt.Errorf("agent: read %s: %w", path, err)
	}
	if len(data) > maxAgentBytes {
		return Definition{}, fmt.Errorf("agent %s: file exceeds %d bytes", path, maxAgentBytes)
	}
	fm, body, err := splitFrontmatter(data)
	if err != nil {
		return Definition{}, fmt.Errorf("agent %s: %w", path, err)
	}
	var f frontmatter
	if err := yaml.Unmarshal(fm, &f); err != nil {
		return Definition{}, fmt.Errorf("agent %s: invalid frontmatter: %w", path, err)
	}

	name := f.Name
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	if !nameRe.MatchString(name) {
		return Definition{}, fmt.Errorf("agent %s: invalid name %q (want %s)", path, name, nameRe)
	}
	instructions := strings.TrimSpace(string(body))
	if instructions == "" {
		return Definition{}, fmt.Errorf("agent %s: empty instructions (the markdown body)", path)
	}
	if len(f.Tools) == 0 {
		return Definition{}, fmt.Errorf("agent %s: declare at least one tool", path)
	}
	var budget time.Duration
	if f.Budget != "" {
		if budget, err = time.ParseDuration(f.Budget); err != nil {
			return Definition{}, fmt.Errorf("agent %s: bad budget %q: %w", path, f.Budget, err)
		}
	}
	when := strings.TrimSpace(f.When)
	if when == "" {
		when = "manual"
	}
	return Definition{
		Name: name, Description: strings.TrimSpace(f.Description), Instructions: instructions,
		Model: f.Model, Tools: f.Tools, When: when, Budget: budget,
	}, nil
}

// splitFrontmatter separates a leading `---`-delimited YAML block from the
// markdown body. The file must open with a `---` line; the block ends at the next
// line that is exactly `---` (or `...`). Mirrors the SKILL.md convention.
func splitFrontmatter(src []byte) (fm, body []byte, err error) {
	rest, ok := bytes.CutPrefix(bytes.TrimLeft(src, "\ufeff \t\r\n"), []byte("---\n"))
	if !ok {
		return nil, nil, errors.New("missing --- frontmatter block")
	}
	start := 0
	for start <= len(rest) {
		nl := bytes.IndexByte(rest[start:], '\n')
		line := rest[start:]
		if nl >= 0 {
			line = rest[start : start+nl]
		}
		if t := bytes.TrimRight(line, "\r"); bytes.Equal(t, []byte("---")) || bytes.Equal(t, []byte("...")) {
			fm = rest[:start]
			if nl < 0 {
				return fm, nil, nil
			}
			return fm, rest[start+nl+1:], nil
		}
		if nl < 0 {
			break
		}
		start += nl + 1
	}
	return nil, nil, errors.New("unterminated frontmatter (no closing ---)")
}
